package main

import (
	"encoding/xml"
	"flag"
	"fmt"
	"github.com/ecwws/lisabot/lisaclient"
	"github.com/ecwws/lisabot/logging"
	"github.com/tbruyelle/hipchat-go/hipchat"
	"os"
	"strings"
	"time"
)

const (
	hipchatHost = "chat.hipchat.com"
	hipchatConf = "conf.hipchat.com"
)

type hipchatClient struct {
	username string
	password string
	resource string
	id       string
	nick     string

	// private
	usersByMention  map[string]*hipchat.User
	usersById       map[int]*hipchat.User
	usersByName     map[string]*hipchat.User
	usersByJid      map[string]*hipchat.User
	xmpp            *xmppConn
	receivedMessage chan *message
	roomsByName     map[string]string
	roomsById       map[string]string
	host            string
	jid             string
	accountId       string
	apiHost         string
	chatHost        string
	mucHost         string
	webHost         string
	token           string
	// tokenExp        int64
	mention   string
	api       *hipchat.Client
	hipchatId int
}

type message struct {
	From        string
	To          string
	Body        string
	MentionName string
}

type xmppMessage struct {
	XMLName  xml.Name `xml:"message"`
	Type     string   `xml:"type,attr"`
	From     string   `xml:"from,attr"`
	To       string   `xml:"to,attr"`
	Id       string   `xml:"id,attr"`
	Body     string   `xml:"body"`
	RoomName string   `xml:"x>name,omitempty"`
	RoomId   string   `xml:"x>id,omitempty"`
}

var logger *logging.LisaLog

func main() {

	user := flag.String("user", "", "hipchat username")
	pass := flag.String("pass", "", "hipchat password")
	nick := flag.String("nick", "Lisa Bot", "hipchat full name")
	server := flag.String("server", "127.0.0.1", "lisabot server")
	port := flag.String("port", "4517", "lisabot server port")
	sourceid := flag.String("id", "lisabot-hipchat", "source id")
	loglevel := flag.String("loglevel", "warn", "loglevel")

	flag.Parse()

	var err error

	logger, err = logging.NewLogger(os.Stdout, *loglevel)

	if err != nil {
		fmt.Println("Error initializing logger: ", err)
		os.Exit(-1)
	}

	conn, err := xmppConnect(hipchatHost)

	if err != nil {
		logger.Error.Println("Error connecting to lisabot:", err)
		os.Exit(1)
	}

	logger.Info.Println("Connected")

	hc := &hipchatClient{
		username: *user,
		password: *pass,
		resource: "bot",
		id:       *user + "@" + hipchatHost,
		nick:     *nick,

		xmpp:            conn,
		usersByMention:  make(map[string]*hipchat.User),
		usersById:       make(map[int]*hipchat.User),
		usersByJid:      make(map[string]*hipchat.User),
		usersByName:     make(map[string]*hipchat.User),
		receivedMessage: make(chan *message),
		host:            hipchatHost,
		roomsByName:     make(map[string]string),
		roomsById:       make(map[string]string),
	}

	err = hc.initialize()

	if err != nil {
		panic(err)
	}
	logger.Info.Println("Authenticated")

	lisa, err := lisaclient.NewClient(*server, *port, logger)

	if err != nil {
		logger.Error.Println("Failed to create lisabot-hipchate:", err)
		os.Exit(2)
	}

	err = lisa.Engage("adapter", *sourceid)

	if err != nil {
		logger.Error.Println("Failed to engage:", err)
		os.Exit(3)
	}

	logger.Info.Println("LisaBot engaged")

	// quit := make(chan int)

	rooms := hc.xmpp.Discover(hc.jid, hc.mucHost)

	autojoin := make([]string, 0, len(rooms))

	for _, room := range rooms {
		hc.roomsByName[room.Name] = room.Id
		hc.roomsById[room.Id] = room.Name
		autojoin = append(autojoin, room.Id)
	}

	hc.api = hipchat.NewClient(hc.token)

	logger.Debug.Println("hc api:", hc.api)

	if err := hc.userLookUp(*user); err != nil {
		logger.Error.Fatal("Unable to lookup info on myself:", err)
	}

	hc.mention = hc.usersByName[hc.nick].MentionName
	hc.hipchatId = hc.usersByName[hc.nick].ID

	hc.xmpp.Join(hc.jid, hc.nick, autojoin)

	hc.xmpp.Available(hc.jid)

	run(lisa, hc)
	// go hc.keepAlive()

	// <-quit
}

func (c *hipchatClient) initialize() error {
	c.xmpp.StreamStart(c.id, c.host)
	for {
		element, err := c.xmpp.RecvNext()

		if err != nil {
			return err
		}

		switch element.Name.Local + element.Name.Space {
		case "stream" + xmppNsStream:
			features := c.xmpp.RecvFeatures()
			if features.StartTLS != nil {
				c.xmpp.StartTLS()
			} else {
				info, err := c.xmpp.Auth(c.username, c.password, c.resource)
				if err != nil {
					return err
				}
				c.jid = info.Jid
				c.accountId = strings.Split(c.jid, "_")[0]
				c.apiHost = info.ApiHost
				c.chatHost = info.ChatHost
				c.mucHost = info.MucHost
				c.webHost = info.WebHost
				c.token = info.Token
				// c.tokenExp = time.Now().Unix() + 2592000
				logger.Debug.Println("JID:", c.jid)
				logger.Debug.Println("Token:", info.Token)
				return nil
			}
		case "proceed" + xmppNsTLS:
			c.xmpp.UseTLS(c.host)
			c.xmpp.StreamStart(c.id, c.host)
			if logger.Level == "debug" {
				c.xmpp.Debug()
			}
		}

	}
	return nil
}

func (c *hipchatClient) keepAlive(trigger chan<- bool) {
	for _ = range time.Tick(60 * time.Second) {
		trigger <- true
	}
}

func run(lisa *lisaclient.LisaClient, hc *hipchatClient) {
	messageFromHC := make(chan *xmppMessage)
	go hc.listen(messageFromHC)

	fromLisa := make(chan *lisaclient.Query)
	toLisa := make(chan *lisaclient.Query)
	go lisa.Run(toLisa, fromLisa)

	keepAlive := make(chan bool)
	go hc.keepAlive(keepAlive)

mainLoop:
	for {
		select {
		case msg := <-messageFromHC:
			logger.Debug.Println("Type:", msg.Type)
			logger.Debug.Println("From:", msg.From)
			logger.Debug.Println("Message:", msg.Body)
			logger.Debug.Println("Room Invite:", msg.RoomName)

			fromSplit := strings.Split(msg.From, "/")
			fromRoom := fromSplit[0]
			var fromNick string
			if len(fromSplit) > 1 {
				fromNick = fromSplit[1]
			}
			if msg.Body != "" && fromNick != hc.nick {
				toLisa <- &lisaclient.Query{
					Type:   "message",
					Source: lisa.SourceId,
					Message: &lisaclient.MessageBlock{
						Message: msg.Body,
						From:    fromNick,
						Room:    hc.roomsById[fromRoom],
					},
				}
			} else if msg.RoomName != "" {
				hc.roomsByName[msg.RoomName] = msg.From
				hc.roomsById[msg.From] = msg.RoomName
				hc.xmpp.Join(hc.jid, hc.nick, []string{msg.From})
			}
		case query := <-fromLisa:
			logger.Debug.Println("Query received:", *query)
			switch {
			case query.Type == "command" &&
				query.Command.Action == "disengage":
				// either server forcing disengage or server connection lost
				logger.Warn.Println("Disengage received, terminating...")
				break mainLoop
			case query.Type == "message":
				hc.groupMessage(hc.roomsByName[query.Message.Room],
					query.Message.Message)
			}
		case <-keepAlive:
			hc.xmpp.KeepAlive()
			logger.Debug.Println("KeepAlive sent")
			// within 60 seconds of token expiration
			// if hc.tokenExp < time.Now().Unix()+60 {
			if true {
				hc.xmpp.AuthRequest(hc.username, hc.password, hc.resource)
				logger.Info.Println("New token requested")
			}
		}
	}
}

func (c *hipchatClient) groupMessage(room, message string) error {
	return c.xmpp.Encode(&xmppMessage{
		From: c.jid,
		To:   room + "/" + c.nick,
		Id:   lisaclient.RandomId(),
		Type: "groupchat",
		Body: message,
	})
}

func (c *hipchatClient) listen(msgChan chan<- *xmppMessage) {

	for {
		element, err := c.xmpp.RecvNext()

		if err != nil {
			continue
		}

		switch element.Name.Local {
		case "message":
			message := new(xmppMessage)
			c.xmpp.DecodeElement(message, &element)
			msgChan <- message

			logger.Debug.Println(*message)
		// case "success":
		//  var auth authResponse
		//  c.xmpp.AuthResp(&auth, &element)
		//  if auth.Token != "" {
		//   c.token = auth.Token
		//   c.tokenExp = time.Now().Unix() + 2592000
		//   logger.Debug.Println("New token:", c.token)
		//  }
		default:
			c.xmpp.Skip()
		}

	}
}

func (c *hipchatClient) userLookUp(name string) error {
	info, _, err := c.api.User.View(name)

	if err != nil {
		return err
	}

	logger.Debug.Println("User found:", info)

	c.usersByMention[info.MentionName] = info
	c.usersById[info.ID] = info
	c.usersByJid[info.XmppJid] = info
	c.usersByName[info.Name] = info

	return nil
}

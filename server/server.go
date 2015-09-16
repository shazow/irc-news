package server

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/shazow/irc-news/user"
	"github.com/sorcix/irc"
)

var ErrHandshakeFailed = errors.New("handshake failed")

const serverName = "irc-news"

func ID(s string) string {
	return strings.ToLower(s)
}

type Server interface {
	Join(user.User) error
	Prefix() *irc.Prefix
}

func New() Server {
	return &server{
		users:    map[string]user.User{},
		channels: map[string]Channel{},
		created:  time.Now(),
	}
}

type server struct {
	sync.RWMutex
	users    map[string]user.User
	channels map[string]Channel
	prefix   *irc.Prefix
	count    int
	created  time.Time
}

// Prefix returns the server's command prefix string.
func (s *server) Prefix() *irc.Prefix {
	return &irc.Prefix{Name: serverName}
}

// HasChannel returns whether a given channel already exists.
func (s *server) HasChannel(name string) bool {
	s.RLock()
	_, exists := s.channels[ID(name)]
	s.RUnlock()
	return exists
}

// Channel returns an existing or new channel with the give name.
func (s *server) Channel(name string) Channel {
	s.Lock()
	id := ID(name)
	ch, ok := s.channels[id]
	if !ok {
		ch = NewChannel(s, name)
		id = ch.ID()
		s.channels[id] = ch
	}
	s.Unlock()
	return ch
}

// Join starts the handshake for a new user.User and returns when complete or failed.
func (s *server) Join(u user.User) error {
	err := s.handshake(u)
	if err != nil {
		return err
	}
	go s.handle(u)
	return nil
}

func (s *server) guestNick() string {
	s.Lock()
	defer s.Unlock()

	s.count++
	return fmt.Sprintf("Guest%s", s.count)
}

// names lists all names for a given channel
func (s *server) names(u user.User, channels ...string) []*irc.Message {
	// TODO: Support full list?
	r := []*irc.Message{}
	for _, channel := range channels {
		if !s.HasChannel(channel) {
			continue
		}
		ch := s.Channel(channel)
		msg := irc.Message{
			Prefix:   s.Prefix(),
			Command:  irc.RPL_NAMREPLY,
			Params:   []string{u.Nick(), "=", channel},
			Trailing: strings.Join(ch.Names(), " "),
		}
		r = append(r, &msg)
	}
	endParams := []string{u.Nick()}
	if len(channels) == 1 {
		endParams = append(endParams, channels[0])
	}
	// FIXME: Do we need to return an ENDOFNAMES for each channel when there are >1 queried?
	r = append(r, &irc.Message{
		Prefix:   s.Prefix(),
		Params:   endParams,
		Command:  irc.RPL_ENDOFNAMES,
		Trailing: "End of /NAMES list.",
	})
	return r
}

func (s *server) handle(u user.User) {
	defer u.Close()
	for {
		msg, err := u.Decode()
		if err != nil {
			logger.Errorf("handle decode error for %s: %s", u.ID(), err.Error())
			return
		}
		switch msg.Command {
		case irc.QUIT:
			// TODO: Respond with ERROR message per RFC?
			return
		case irc.PING:
			err = u.Encode(&irc.Message{
				Prefix:  s.Prefix(),
				Command: irc.PONG,
				Params:  msg.Params,
			})
		case irc.JOIN:
			err = u.Encode(&irc.Message{
				Prefix:   s.Prefix(),
				Command:  irc.ERR_INVITEONLYCHAN,
				Trailing: "Cannot join channel (+i)",
			})
		case irc.NAMES:
			if len(msg.Params) < 1 {
				continue
			}
			err = u.EncodeMany(s.names(u, msg.Params[0])...)
		case irc.PRIVMSG:
			// TODO: ...
			continue
		}
		if err != nil {
			logger.Errorf("handle encode error for %s: %s", u.ID(), err.Error())
			return
		}
	}
}

func (s *server) add(u user.User) (ok bool) {
	s.Lock()
	defer s.Unlock()

	id := u.ID()
	if _, exists := s.users[id]; exists {
		return false
	}

	s.users[id] = u
	return true
}

func (s *server) handshake(u user.User) error {
	// Read messages until we filled in USER details.
	identity := user.Identity{}
	for i := 5; i > 0; i-- {
		// Consume 5 messages then give up.
		msg, err := u.Decode()
		if err != nil {
			return err
		}
		if len(msg.Params) < 1 {
			continue
		}
		switch msg.Command {
		case irc.NICK:
			identity.Nick = msg.Params[0]
		case irc.USER:
			identity.User = msg.Params[0]
			identity.Real = msg.Trailing
			u.Set(identity)
			ok := s.add(u)
			if !ok {
				identity.Nick = s.guestNick()
				ok = s.add(u)
			}
			if !ok {
				return ErrHandshakeFailed
			}
			return u.Encode(&irc.Message{
				Prefix:   s.Prefix(),
				Command:  irc.RPL_WELCOME,
				Params:   []string{identity.User},
				Trailing: fmt.Sprintf("Welcome!"),
			})
		}
	}
	return ErrHandshakeFailed
}
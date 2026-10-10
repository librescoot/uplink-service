// Package wire defines the bounded transport boundary used by the live adapter.
package wire

import "time"

type Token interface {
	WaitTimeout(time.Duration) bool
	Error() error
}
type Message interface {
	Payload() []byte
	Retained() bool
	Qos() byte
	Ack()
}
type MessageHandler func(Client, Message)
type Client interface {
	IsConnected() bool
	Subscribe(string, byte, MessageHandler) Token
	Unsubscribe(...string) Token
	Publish(string, byte, bool, any) Token
	PublishLive([]byte, time.Time, func() bool) Token
}

type Result struct{ Err error }

func (r Result) WaitTimeout(time.Duration) bool { return true }
func (r Result) Error() error                   { return r.Err }

type Frame []byte

func (f Frame) Payload() []byte { return []byte(f) }
func (Frame) Retained() bool    { return false }
func (Frame) Qos() byte         { return 0 }
func (Frame) Ack()              {}

package steam

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"github.com/WirStaff/go-steam/cryptoutil"
	"github.com/WirStaff/go-steam/protocol"
	"golang.org/x/net/proxy"
	"io"
	"net"
	"sync"
	"time"
)

type connection interface {
	Read() (*protocol.Packet, error)
	Write([]byte) error
	Close() error
	SetEncryptionKey([]byte)
	IsEncrypted() bool
}

const tcpConnectionMagic uint32 = 0x31305456 // "VT01"

type tcpConnection struct {
	conn        net.Conn
	ciph        cipher.Block
	cipherMutex sync.RWMutex
}

type ProxyConnection struct {
	addr     string
	username string
	password string
}

func dialTCP(laddr, raddr *net.TCPAddr, p *ProxyConnection) (*tcpConnection, error) {
	var conn net.Conn
	var err error

	if p != nil {
		auth := proxy.Auth{User: p.username, Password: p.password}

		dailer, _ := proxy.SOCKS5("tcp", p.addr, &auth, &net.Dialer{
			Timeout:   60 * time.Second,
			KeepAlive: 360 * time.Second,
		})

		conn, err = dailer.Dial("tcp", raddr.String())
	} else {
		conn, err = net.DialTCP("tcp", laddr, raddr)
	}

	if err != nil {
		return nil, err
	}

	return &tcpConnection{
		conn: conn,
	}, nil
}

func (c *tcpConnection) Read() (*protocol.Packet, error) {
	// All packets begin with a packet length
	var packetLen uint32
	err := binary.Read(c.conn, binary.LittleEndian, &packetLen)
	if err != nil {
		return nil, err
	}

	// A magic value follows for validation
	var packetMagic uint32
	err = binary.Read(c.conn, binary.LittleEndian, &packetMagic)
	if err != nil {
		return nil, err
	}
	if packetMagic != tcpConnectionMagic {
		return nil, fmt.Errorf("Invalid connection magic! Expected %d, got %d!", tcpConnectionMagic, packetMagic)
	}

	buf := make([]byte, packetLen, packetLen)
	_, err = io.ReadFull(c.conn, buf)
	if err == io.ErrUnexpectedEOF {
		return nil, io.EOF
	}
	if err != nil {
		return nil, err
	}

	// Packets after ChannelEncryptResult are encrypted
	c.cipherMutex.RLock()
	if c.ciph != nil {
		buf = cryptoutil.SymmetricDecrypt(c.ciph, buf)
	}
	c.cipherMutex.RUnlock()

	return protocol.NewPacket(buf)
}

// Writes a message. This may only be used by one goroutine at a time.
func (c *tcpConnection) Write(message []byte) error {
	c.cipherMutex.RLock()
	if c.ciph != nil {
		message = cryptoutil.SymmetricEncrypt(c.ciph, message)
	}
	c.cipherMutex.RUnlock()

	err := binary.Write(c.conn, binary.LittleEndian, uint32(len(message)))
	if err != nil {
		return err
	}
	err = binary.Write(c.conn, binary.LittleEndian, tcpConnectionMagic)
	if err != nil {
		return err
	}

	_, err = c.conn.Write(message)
	return err
}

func (c *tcpConnection) Close() error {
	return c.conn.Close()
}

func (c *tcpConnection) SetEncryptionKey(key []byte) {
	c.cipherMutex.Lock()
	defer c.cipherMutex.Unlock()
	if key == nil {
		c.ciph = nil
		return
	}
	if len(key) != 32 {
		panic("Connection AES key is not 32 bytes long!")
	}

	var err error
	c.ciph, err = aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
}

func (c *tcpConnection) IsEncrypted() bool {
	c.cipherMutex.RLock()
	defer c.cipherMutex.RUnlock()
	return c.ciph != nil
}

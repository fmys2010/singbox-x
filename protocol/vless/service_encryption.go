package vless

// Derived from github.com/sagernet/sing-vmess/vless service.go: the same server logic, except that
// XTLS Vision runs on top of the VLESS encryption layer instead of a TLS connection.

import (
	"context"
	"encoding/binary"
	"io"
	"net"

	"github.com/sagernet/sing-box/common/vlessenc"
	"github.com/sagernet/sing-vmess"
	vlessproto "github.com/sagernet/sing-vmess/vless"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/gofrs/uuid/v5"
)

type encService[T comparable] struct {
	userMap  map[[16]byte]T
	userFlow map[T]string
	logger   logger.Logger
	handler  Handler
}

type Handler interface {
	N.TCPConnectionHandlerEx
	N.UDPConnectionHandlerEx
}

func newEncService[T comparable](logger logger.Logger, handler Handler) *encService[T] {
	return &encService[T]{
		logger:  logger,
		handler: handler,
	}
}

func (s *encService[T]) UpdateUsers(userList []T, userUUIDList []string, userFlowList []string) {
	userMap := make(map[[16]byte]T)
	userFlowMap := make(map[T]string)
	for i, userName := range userList {
		userID, err := uuid.FromString(userUUIDList[i])
		if err != nil {
			userID = uuid.NewV5(uuid.Nil, userUUIDList[i])
		}
		userMap[userID] = userName
		userFlowMap[userName] = userFlowList[i]
	}
	s.userMap = userMap
	s.userFlow = userFlowMap
}

func (s *encService[T]) NewConnection(ctx context.Context, conn net.Conn, source M.Socksaddr, onClose N.CloseHandlerFunc) error {
	request, err := vlessproto.ReadRequest(conn)
	if err != nil {
		return err
	}
	user, loaded := s.userMap[request.UUID]
	if !loaded {
		return E.New("unknown UUID: ", uuid.FromBytesOrNil(request.UUID[:]))
	}
	ctx = auth.ContextWithUser(ctx, user)
	userFlow := s.userFlow[user]
	if request.Flow == vlessproto.FlowVision && request.Command == vmess.NetworkUDP {
		return E.New(vlessproto.FlowVision, " flow does not support UDP")
	} else if request.Flow != userFlow {
		return E.New("flow mismatch: expected ", flowName(userFlow), ", but got ", flowName(request.Flow))
	}

	if request.Command == vmess.CommandUDP {
		s.handler.NewPacketConnectionEx(ctx, &encServerPacketConn{ExtendedConn: bufio.NewExtendedConn(conn), destination: request.Destination}, source, request.Destination, onClose)
		return nil
	}
	responseConn := &encServerConn{ExtendedConn: bufio.NewExtendedConn(conn)}
	switch userFlow {
	case vlessproto.FlowVision:
		encryptedConn, isEncrypted := conn.(*vlessenc.CommonConn)
		if !isEncrypted {
			return E.New("vision requires vless decryption on this inbound")
		}
		conn = newEncryptedVisionConn(responseConn, encryptedConn, request.UUID, s.logger)
	case "":
		conn = responseConn
	default:
		return E.New("unknown flow: ", userFlow)
	}
	switch request.Command {
	case vmess.CommandTCP:
		s.handler.NewConnectionEx(ctx, conn, source, request.Destination, onClose)
		return nil
	case vmess.CommandMux:
		return vmess.HandleMuxConnection(ctx, conn, source, s.handler)
	default:
		return E.New("unknown command: ", request.Command)
	}
}

func flowName(value string) string {
	if value == "" {
		return "none"
	}
	return value
}

type encServerConn struct {
	N.ExtendedConn
	responseWritten bool
}

func (c *encServerConn) Read(b []byte) (n int, err error) {
	return c.ExtendedConn.Read(b)
}

func (c *encServerConn) Write(b []byte) (n int, err error) {
	if !c.responseWritten {
		buffer := buf.NewSize(2 + len(b))
		buffer.WriteByte(vlessproto.Version)
		buffer.WriteByte(0)
		buffer.Write(b)
		_, err = c.ExtendedConn.Write(buffer.Bytes())
		buffer.Release()
		if err == nil {
			n = len(b)
		}
		c.responseWritten = true
		return
	}
	return c.ExtendedConn.Write(b)
}

func (c *encServerConn) WriteBuffer(buffer *buf.Buffer) error {
	if !c.responseWritten {
		header := buffer.ExtendHeader(2)
		header[0] = vlessproto.Version
		header[1] = 0
		c.responseWritten = true
	}
	return c.ExtendedConn.WriteBuffer(buffer)
}

func (c *encServerConn) FrontHeadroom() int {
	if c.responseWritten {
		return 0
	}
	return 2
}

func (c *encServerConn) ReaderReplaceable() bool {
	return true
}

func (c *encServerConn) WriterReplaceable() bool {
	return c.responseWritten
}

func (c *encServerConn) NeedAdditionalReadDeadline() bool {
	return true
}

func (c *encServerConn) Upstream() any {
	return c.ExtendedConn
}

type encServerPacketConn struct {
	N.ExtendedConn
	responseWritten bool
	destination     M.Socksaddr
}

func (c *encServerPacketConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	var packetLen uint16
	err = binary.Read(c.ExtendedConn, binary.BigEndian, &packetLen)
	if err != nil {
		return
	}
	if len(p) < int(packetLen) {
		err = io.ErrShortBuffer
		return
	}
	n, err = io.ReadFull(c.ExtendedConn, p[:packetLen])
	if err != nil {
		return
	}
	if c.destination.IsFqdn() {
		addr = c.destination
	} else {
		addr = c.destination.UDPAddr()
	}
	return
}

func (c *encServerPacketConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	if !c.responseWritten {
		_, err = c.ExtendedConn.Write([]byte{vlessproto.Version, 0})
		if err != nil {
			return
		}
		c.responseWritten = true
	}
	err = binary.Write(c.ExtendedConn, binary.BigEndian, uint16(len(p)))
	if err != nil {
		return
	}
	return c.ExtendedConn.Write(p)
}

func (c *encServerPacketConn) ReadPacket(buffer *buf.Buffer) (destination M.Socksaddr, err error) {
	var packetLen uint16
	err = binary.Read(c.ExtendedConn, binary.BigEndian, &packetLen)
	if err != nil {
		return
	}

	_, err = buffer.ReadFullFrom(c.ExtendedConn, int(packetLen))
	if err != nil {
		return
	}

	destination = c.destination
	return
}

func (c *encServerPacketConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	if !c.responseWritten {
		_, err := c.ExtendedConn.Write([]byte{vlessproto.Version, 0})
		if err != nil {
			return err
		}
		c.responseWritten = true
	}
	packetLen := buffer.Len()
	binary.BigEndian.PutUint16(buffer.ExtendHeader(2), uint16(packetLen))
	return c.ExtendedConn.WriteBuffer(buffer)
}

func (c *encServerPacketConn) FrontHeadroom() int {
	return 2
}

func (c *encServerPacketConn) NeedAdditionalReadDeadline() bool {
	return true
}

func (c *encServerPacketConn) Upstream() any {
	return c.ExtendedConn
}

package vlessenc

import (
	"bytes"
	"encoding/base64"
	"strconv"
	"strings"

	E "github.com/sagernet/sing/common/exceptions"
)

// NewClient parses the client side "encryption" string of Xray-core's VLESS:
//
//	mlkem768x25519plus.<native|xorpub|random>.<1rtt|0rtt>[.<padding>...].<key>[.<key>...]
//
// where every key is a base64url X25519 public key (32 bytes) or ML-KEM-768
// encapsulation key (1184 bytes), and padding is "len-min-max" triples.
func NewClient(encryption string) (*ClientInstance, error) {
	parts := strings.Split(encryption, ".")
	if len(parts) < 4 || parts[0] != "mlkem768x25519plus" {
		return nil, E.New("unsupported vless encryption: ", encryption)
	}
	var xorMode uint32
	switch parts[1] {
	case "native":
	case "xorpub":
		xorMode = 1
	case "random":
		xorMode = 2
	default:
		return nil, E.New("unsupported vless encryption mode: ", parts[1])
	}
	var seconds uint32
	switch parts[2] {
	case "1rtt":
	case "0rtt":
		seconds = 1
	default:
		return nil, E.New("unsupported vless encryption rtt: ", parts[2])
	}
	var paddings []string
	var keys [][]byte
	for _, part := range parts[3:] {
		if len(part) < 20 {
			paddings = append(paddings, part)
			continue
		}
		key, err := base64.RawURLEncoding.DecodeString(part)
		if err != nil || (len(key) != 32 && len(key) != 1184) {
			return nil, E.New("invalid vless encryption key")
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, E.New("vless encryption: missing server key")
	}
	instance := new(ClientInstance)
	err := instance.Init(keys, xorMode, seconds, strings.Join(paddings, "."))
	if err != nil {
		return nil, E.Cause(err, "vless encryption")
	}
	return instance, nil
}

// Input returns the decrypted but not yet consumed bytes, Vision hands
// them over when it switches to direct copy.
func (c *CommonConn) Input() *bytes.Reader {
	return &c.input
}

// NewServer parses the server side "decryption" string of Xray-core's VLESS:
//
//	mlkem768x25519plus.<native|xorpub|random>.<seconds>s|<from>-<to>s[.<padding>...].<private key>[.<private key>...]
//
// where every private key is a base64url X25519 private key (32 bytes) or ML-KEM-768 seed (64 bytes).
func NewServer(decryption string) (*ServerInstance, error) {
	parts := strings.Split(decryption, ".")
	if len(parts) < 4 || parts[0] != "mlkem768x25519plus" {
		return nil, E.New("unsupported vless decryption: ", decryption)
	}
	var xorMode uint32
	switch parts[1] {
	case "native":
	case "xorpub":
		xorMode = 1
	case "random":
		xorMode = 2
	default:
		return nil, E.New("unsupported vless decryption mode: ", parts[1])
	}
	seconds := strings.SplitN(strings.TrimSuffix(parts[2], "s"), "-", 2)
	secondsFrom, err := strconv.ParseInt(seconds[0], 10, 64)
	if err != nil {
		return nil, E.New("invalid vless decryption ticket time: ", parts[2])
	}
	var secondsTo int64
	if len(seconds) == 2 {
		secondsTo, err = strconv.ParseInt(seconds[1], 10, 64)
		if err != nil {
			return nil, E.New("invalid vless decryption ticket time: ", parts[2])
		}
	}
	var paddings []string
	var keys [][]byte
	for _, part := range parts[3:] {
		if len(part) < 20 {
			paddings = append(paddings, part)
			continue
		}
		key, err := base64.RawURLEncoding.DecodeString(part)
		if err != nil || (len(key) != 32 && len(key) != 64) {
			return nil, E.New("invalid vless decryption key")
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, E.New("vless decryption: missing private key")
	}
	instance := new(ServerInstance)
	err = instance.Init(keys, xorMode, secondsFrom, secondsTo, strings.Join(paddings, "."))
	if err != nil {
		return nil, E.Cause(err, "vless decryption")
	}
	return instance, nil
}

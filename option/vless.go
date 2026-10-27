package option

type VLESSInboundOptions struct {
	ListenOptions
	Users []VLESSUser `json:"users,omitempty"`
	// Decryption is Xray-core's VLESS decryption, e.g. "mlkem768x25519plus.native.600s.<private key>".
	Decryption string `json:"decryption,omitempty"`
	InboundTLSOptionsContainer
	Multiplex *InboundMultiplexOptions `json:"multiplex,omitempty"`
	Transport *V2RayTransportOptions   `json:"transport,omitempty"`
}

type VLESSUser struct {
	Name string `json:"name,omitempty"`
	UUID string `json:"uuid"`
	Flow string `json:"flow,omitempty"`
}

type VLESSOutboundOptions struct {
	DialerOptions
	ServerOptions
	UUID    string      `json:"uuid"`
	Flow    string      `json:"flow,omitempty"`
	Network NetworkList `json:"network,omitempty"`
	// Encryption is Xray-core's VLESS encryption, e.g. "mlkem768x25519plus.native.0rtt.<key>".
	Encryption string `json:"encryption,omitempty"`
	OutboundTLSOptionsContainer
	Multiplex      *OutboundMultiplexOptions `json:"multiplex,omitempty"`
	Transport      *V2RayTransportOptions    `json:"transport,omitempty"`
	PacketEncoding *string                   `json:"packet_encoding,omitempty"`
}

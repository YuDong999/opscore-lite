package connection

type MessageType string

func setUint32(buffer []byte, value uint32) {
	buffer[0] = byte(value & 0xff)
	buffer[1] = byte(value >> 8 & 0xff)
	buffer[2] = byte(value >> 16 & 0xff)
	buffer[3] = byte(value >> 24 & 0xff)
}

func getUint32(buffer []byte) uint32 {
	return uint32(buffer[0]) |
		uint32(buffer[1])<<8 |
		uint32(buffer[2])<<16 |
		uint32(buffer[3])<<24
}

const (
	CONNECT    MessageType = "\x43\x4e"
	HANDSHAKE  MessageType = "\x48\x53"
	DISCONNECT MessageType = "\x44\x43"

	GLOBAL_GET   MessageType = "\x41\xc2"
	GLOBAL_SET   MessageType = "\x42\xc2"
	GLOBAL_KILL  MessageType = "\x43\xc2"
	GLOBAL_ORDER MessageType = "\x45\xc2"
	GLOBAL_DATA  MessageType = "\x49\xc2"

	CLASSMETHOD_VALUE MessageType = "\x4b\xc2"
	CLASSMETHOD_VOID  MessageType = "\x4c\xc2"

	METHOD_VALUE MessageType = "\x5b\xc2"
	METHOD_VOID  MessageType = "\x5c\xc2"

	PROPERTY_GET MessageType = "\x5d\xc2"
	PROPERTY_SET MessageType = "\x5e\xc2"

	DIRECT_QUERY            MessageType = "DQ"
	PREPARED_QUERY          MessageType = "PQ"
	DIRECT_UPDATE           MessageType = "DU"
	PREPARED_UPDATE         MessageType = "PU"
	PREPARE                 MessageType = "PP"
	GET_AUTO_GENERATED_KEYS MessageType = "GG"

	COMMIT   MessageType = "TC"
	ROLLBACK MessageType = "TR"

	MULTIPLE_RESULT_SETS_FETCH_DATA MessageType = "MD"
	GET_MORE_RESULTS                MessageType = "MR"
	FETCH_DATA                      MessageType = "FD"
	GET_SERVER_ERROR                MessageType = "OE"
)

type MessageHeader struct {
	header [14]byte
}

func NewMessageHeader(messageType MessageType) MessageHeader {
	header := [14]byte{}
	header[12] = messageType[0]
	header[13] = messageType[1]
	return MessageHeader{header}
}

func (mh *MessageHeader) GetStatus() uint16 {
	return uint16(mh.header[12]) | (uint16(mh.header[13]) << 8)
}

func (mh *MessageHeader) SetLength(length uint32) {
	setUint32(mh.header[0:], length)
}

func (mh MessageHeader) GetLength() uint32 {
	return getUint32(mh.header[0:])
}

func (mh *MessageHeader) SetCount(cnt uint32) {
	setUint32(mh.header[4:], cnt)
}

func (mh *MessageHeader) SetStatementId(statementId uint32) {
	setUint32(mh.header[8:], statementId)
}

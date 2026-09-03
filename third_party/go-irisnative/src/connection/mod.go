package connection

import (
	"database/sql/driver"
	"errors"
	"net"
)

const VERSION_PROTOCOL uint16 = 69

type Connection struct {
	conn           *net.TCPConn
	messageCount   uint32
	statement      uint32
	unicode        bool
	locale         string
	version        uint16
	info           string
	featureOptions uint
	tx             bool
}

var (
	ErrCouldNotDetectUsername = errors.New("intersystems: Could not detect default username. Please provide one explicitly")
	errBeginTx                = errors.New("could not begin transaction")
	errMultipleTx             = errors.New("multiple transactions")
	errReadOnlyTxNotSupported = errors.New("read-only transactions are not supported")
)

func Connect(addr string, namespace, login, password string) (connection Connection, err error) {

	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return
	}

	conn, err := net.DialTCP("tcp", nil, tcpAddr)
	if err != nil {
		return
	}

	connection = Connection{
		conn: conn,
	}

	if err = connection.handshake(); err != nil {
		return
	}

	if err = connection.connect(namespace, login, password); err != nil {
		return
	}

	// fmt.Println(connection.version, connection.info)

	return
}

func (c *Connection) Disconnect() {
	if c.conn == nil {
		return
	}
	msg := NewMessage(DISCONNECT)
	_, _ = c.conn.Write(msg.Dump(c.count()))
	_ = c.conn.Close()
	c.conn = nil
}

func (c *Connection) count() uint32 {
	count := c.messageCount
	c.messageCount += 1
	return count
}

func (c *Connection) statementId() uint32 {
	statement := c.statement
	c.statement += 1
	return statement
}

func (c *Connection) handshake() (err error) {
	var message = NewMessage(HANDSHAKE)
	message.AddRaw(VERSION_PROTOCOL)

	_, err = c.conn.Write(message.Dump(c.count()))
	if err != nil {
		return
	}

	msg, err := ReadMessage(c.conn)
	if err != nil {
		return
	}

	var version uint16
	msg.GetRaw(&version)
	c.version = version

	var unicode uint16
	msg.GetRaw(&unicode)
	c.unicode = unicode == 1

	var locale string
	msg.Get(&locale)
	c.locale = locale
	return
}

func encode(value string) []byte {
	in := []byte(value)
	length := len(in)
	out := make([]byte, length)
	for i := range in {
		length--
		temp := ((int(in[i])^0xa7)&0xff + length) & 0xff
		out[length] = byte(temp<<5 | temp>>3)
	}
	return out
}

type FeatureOption uint

const (
	OptionNone                FeatureOption = 0
	OptionFastSelect          FeatureOption = 1
	OptionFastInsert          FeatureOption = 2
	OptionFastSelectAndInsert FeatureOption = 3
	OptionDurableTransactions FeatureOption = 4
	OptionNotNullable         FeatureOption = 8
	OptionRedirectOutput      FeatureOption = 32
)

func (c *Connection) IsOptionFastInsert() bool {
	return c.featureOptions&uint(OptionFastInsert) == uint(OptionFastInsert)
}

func (c *Connection) IsOptionFastSelect() bool {
	return c.featureOptions&uint(OptionFastSelect) == uint(OptionFastSelect)
}

func (c *Connection) connect(namespace, login, password string) (err error) {
	msg := NewMessage(CONNECT)
	msg.Set(namespace)
	msg.Set(encode(login))
	msg.Set(encode(password))
	var user = "go"
	if user, err = systemUser(); err != nil {
		user = "go"
	}
	msg.Set(user)            // machine user name
	msg.Set("go-machine")    // machine name
	msg.Set("libirisnative") // application name
	msg.Set("")              // ?
	msg.Set("go")            // SharedMemoryFlag?
	msg.Set("")              // EventClass
	msg.Set(1)               // AutoCommit ? 1 : 2
	msg.Set(0)               // IsolationLevel
	var featureOptions = OptionNone
	featureOptions += OptionFastSelect
	// Tricky to make it fully working yet
	// featureOptions += OptionFastInsert
	featureOptions += OptionDurableTransactions
	featureOptions += OptionRedirectOutput
	msg.Set(int(featureOptions)) // FeatureOption

	_, err = c.conn.Write(msg.Dump(c.count()))
	if err != nil {
		return
	}

	msg, err = ReadMessage(c.conn)
	if err != nil {
		return
	}
	if status := msg.GetStatus(); status == 417 {
		var errorMsg string
		msg.Get(&errorMsg)
		err = errors.New(errorMsg)
		return
	}

	var info string
	msg.Get(&info)
	c.info = info
	var (
		delimited_ids        bool
		ignored              int
		isolationLevel       int
		serverJobNumber      string
		sqlEmptyString       int
		serverFeatureOptions uint
	)
	msg.Get(&delimited_ids)
	msg.Get(&ignored)
	msg.Get(&isolationLevel)
	msg.Get(&serverJobNumber)
	msg.Get(&sqlEmptyString)
	msg.Get(&serverFeatureOptions)
	c.featureOptions = serverFeatureOptions
	return
}

func systemUser() (string, error) {
	u, err := userCurrent()
	if err != nil {
		return "", err
	}
	return u, nil
}

func (c *Connection) Commit() (err error) {
	msg := NewMessage(COMMIT)
	_, err = c.conn.Write(msg.Dump(c.count()))
	if err != nil {
		return
	}
	_, err = ReadMessage(c.conn)
	if err != nil {
		return
	}
	return
}

func (c *Connection) Rollback() (err error) {
	msg := NewMessage(ROLLBACK)
	_, err = c.conn.Write(msg.Dump(c.count()))
	if err != nil {
		return
	}
	_, err = ReadMessage(c.conn)
	if err != nil {
		return
	}
	return
}

func (c *Connection) BeginTx(opts driver.TxOptions) (driver.Tx, error) {
	if c.tx {
		return nil, errors.Join(errBeginTx, errMultipleTx)
	}

	if opts.ReadOnly {
		return nil, errors.Join(errBeginTx, errReadOnlyTxNotSupported)
	}

	if _, err := c.DirectUpdate("START TRANSACTION"); err != nil {
		return nil, errors.Join(errBeginTx, err)
	}
	c.tx = true
	return &tx{c}, nil
}

package intersystems

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
)

// Connector represents a fixed configuration for the pq driver with a given
// name. Connector satisfies the database/sql/driver Connector interface and
// can be used to create any number of DB Conn's via the database/sql OpenDB
// function.
//
// See https://golang.org/pkg/database/sql/driver/#Connector.
// See https://golang.org/pkg/database/sql/#OpenDB.
type Connector struct {
	opts   values
	// dialer Dialer
}

// Connect returns a connection to the database using the fixed configuration
// of this Connector. Context is not used.
func (c *Connector) Connect(ctx context.Context) (driver.Conn, error) {
	return c.open(ctx)
}

// Driver returns the underlying driver of this Connector.
func (c *Connector) Driver() driver.Driver {
	return &Driver{}
}

// NewConnector returns a connector for the pq driver in a fixed configuration
// with the given dsn. The returned connector can be used to create any number
// of equivalent Conn's. The returned connector is intended to be used with
// database/sql.OpenDB.
//
// See https://golang.org/pkg/database/sql/driver/#Connector.
// See https://golang.org/pkg/database/sql/#OpenDB.
func NewConnector(dsn string) (*Connector, error) {
	var err error
	o := make(values)

	// A number of defaults are applied here, in this order:
	//
	// * Very low precedence defaults applied in every situation
	// * Environment variables
	// * Explicitly passed connection information
	o["host"] = "localhost"
	o["port"] = "1972"

	if strings.HasPrefix(dsn, "iris://") || strings.HasPrefix(dsn, "IRIS://") {
		dsn, err = ParseURL(dsn)
		if err != nil {
			return nil, err
		}
	}

	if err := parseOpts(dsn, o); err != nil {
		return nil, err
	}

	if enc, ok := o["client_encoding"]; ok && !isUTF8(enc) {
		return nil, errors.New("client_encoding must be absent or 'UTF8'")
	}
	o["client_encoding"] = "UTF8"

	return &Connector{opts: o, /*dialer: defaultDialer{}*/}, nil
}

// isUTF8 returns whether name is a fuzzy variation of the string "UTF-8".
func isUTF8(name string) bool {
	s := strings.Map(alnumLowerASCII, name)
	return s == "utf8" || s == "unicode"
}

func alnumLowerASCII(ch rune) rune {
	if 'A' <= ch && ch <= 'Z' {
		return ch + ('a' - 'A')
	}
	if 'a' <= ch && ch <= 'z' || '0' <= ch && ch <= '9' {
		return ch
	}
	return -1 // discard
}

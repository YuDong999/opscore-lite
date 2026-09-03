package connection

import (
	"database/sql/driver"
	"errors"
	"strings"
)

var (
	errNoRowsAffected         = errors.New("no RowsAffected available after the empty statement")
	errNoLastInsertID         = errors.New("no LastInsertId available after the empty statement")
)

type Result struct {
	cn      *Connection
	affected int64
}

func (r Result) LastInsertId() (lastId int64, err error) {
	// var msg Message
	// msg = NewMessage(GET_AUTO_GENERATED_KEYS)
	// msg.header.SetStatementId(r.cn.statementId())
	// _, err = r.cn.conn.Write(msg.Dump(r.cn.count()))
	// if err != nil {
	// 	return
	// }
	// msg, err = ReadMessage(r.cn.conn)
	// if err != nil {
	// 	return
	// }
	// msg.Get(&lastId)
	// return
	var rs *ResultSet
	rs, err = r.cn.DirectQuery("SELECT LAST_IDENTITY()")
	if err != nil {
		return
	}
	row, err := rs.Next()
	if err != nil {
		return
	}
	lastId = int64(row[0].(int))
	return
}

func (r Result) RowsAffected() (int64, error) {
	return r.affected, nil
}

type Rows struct {
	cn *Connection
	rs *ResultSet
}

type noRows struct{}

var emptyRows noRows

var _ driver.Result = noRows{}

func (noRows) LastInsertId() (int64, error) {
	return 0, errNoLastInsertID
}

func (noRows) RowsAffected() (int64, error) {
	return 0, errNoRowsAffected
}


func (r *Rows) Close() error {
	return nil
}

func (r *Rows) Columns() []string {
	if r.rs == nil {
		return []string{}
	}
	columns := r.rs.Columns()
	colNames := make([]string, len(columns))
	for k, c := range columns {
		colname := c.Name()
		// tricking IRIS
		colname = strings.ReplaceAll(colname, "﹒", ".")
		colNames[k] = colname
	}
	// fmt.Printf("Columns: %#v\n", colNames)
	return colNames
}

func (r *Rows) Next(dest []driver.Value) (err error) {
	row, err := r.rs.Next()
	if err != nil {
		return err
	}
	for i := range dest {
		dest[i] = row[i]
	}
	// fmt.Printf("RowsNext: %#v\n", dest)
	return nil
}


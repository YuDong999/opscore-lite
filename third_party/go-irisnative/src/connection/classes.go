package connection

import "github.com/caretdev/go-irisnative/src/iris"

func (c *Connection) ServerVersion() (result string, err error) {
	err = c.ClassMethod("%SYSTEM.Version", "GetVersion", &result)
	return
}

func (c *Connection) ClassMethod(class, method string, result interface{}, args ...interface{}) (err error) {
	msg := NewMessage(CLASSMETHOD_VALUE)
	msg.Set(class)
	msg.Set(method)
	msg.Set(len(args))
	for _, arg := range args {
		msg.Set(arg)
	}

	_, err = c.conn.Write(msg.Dump(c.count()))
	if err != nil {
		return
	}
	msg, err = ReadMessage(c.conn)
	if err != nil {
		return
	}

	msg.Get(result)

	return
}

func (c *Connection) ClassMethodVoid(class, method string, args ...interface{}) (err error) {
	msg := NewMessage(CLASSMETHOD_VOID)
	msg.Set(class)
	msg.Set(method)
	msg.Set(len(args))
	for _, arg := range args {
		msg.Set(arg)
	}

	_, err = c.conn.Write(msg.Dump(c.count()))
	if err != nil {
		return
	}
	msg, err = ReadMessage(c.conn)
	if err != nil {
		return
	}
	return
}

func (c *Connection) Method(obj iris.Oref, method string, result interface{}, args ...interface{}) (err error) {
	msg := NewMessage(METHOD_VALUE)
	msg.Set(obj)
	msg.Set(method)
	msg.Set(len(args))
	for _, arg := range args {
		msg.Set(arg)
	}

	_, err = c.conn.Write(msg.Dump(c.count()))
	if err != nil {
		return
	}
	msg, err = ReadMessage(c.conn)
	if err != nil {
		return
	}

	msg.Get(result)

	return
}

func (c *Connection) MethodVoid(obj, method string, args ...interface{}) (err error) {
	msg := NewMessage(METHOD_VOID)
	msg.Set(obj)
	msg.Set(method)
	msg.Set(len(args))
	for _, arg := range args {
		msg.Set(arg)
	}

	_, err = c.conn.Write(msg.Dump(c.count()))
	if err != nil {
		return
	}
	msg, err = ReadMessage(c.conn)
	if err != nil {
		return
	}
	return
}
func (c *Connection) PropertyGet(obj iris.Oref, property string, result interface{}) (err error) {
	msg := NewMessage(PROPERTY_GET)
	msg.Set(obj)
	msg.Set(property)
	// msg.Set(0)

	_, err = c.conn.Write(msg.Dump(c.count()))
	if err != nil {
		return
	}
	msg, err = ReadMessage(c.conn)
	if err != nil {
		return
	}

	msg.Get(result)

	return
}

package connection

func (c *Connection) GlobalIsDefined(global string, subs ...interface{}) (bool, bool) {
	msg := NewMessage(GLOBAL_DATA)
	msg.Set(global)
	msg.Set(len(subs))
	for _, sub := range subs {
		msg.Set(sub)
	}
	msg.Set(0)
	_, err := c.conn.Write(msg.Dump(c.count()))
	if err != nil {
		return false, false
	}

	msg, err = ReadMessage(c.conn)
	if err != nil {
		return false, false
	}

	var result uint8
	msg.Get(&result)
	return result%10 == 1, result >= 10
}

func (c *Connection) GlobalSet(global string, value interface{}, subs ...interface{}) (err error) {
	msg := NewMessage(GLOBAL_SET)
	msg.Set(global)
	msg.Set(len(subs))
	for _, sub := range subs {
		msg.Set(sub)
	}
	msg.Set(value)

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

func (c *Connection) GlobalKill(global string, subs ...interface{}) (err error) {
	msg := NewMessage(GLOBAL_KILL)
	msg.Set(global)
	msg.Set(len(subs))
	for _, sub := range subs {
		msg.Set(sub)
	}

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

func (c *Connection) GlobalGet(global string, result interface{}, subs ...interface{}) (err error) {
	msg := NewMessage(GLOBAL_GET)
	msg.Set(global)
	msg.Set(len(subs))
	for _, sub := range subs {
		msg.Set(sub)
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

func (c *Connection) GlobalNext(global string, ind *string, subs ...interface{}) (hasNext bool, err error) {
	msg := NewMessage(GLOBAL_ORDER)
	msg.Set(global)
	msg.Set(len(subs) + 1)
	for _, sub := range subs {
		msg.Set(sub)
	}
	msg.Set(*ind)
	msg.Set(3)

	if _, err = c.conn.Write(msg.Dump(c.count())); err != nil {
		return
	}

	if msg, err = ReadMessage(c.conn); err != nil {
		return
	}

	var result string
	msg.Get(&result)
	*ind = result
	hasNext = result != ""

	return
}

func (c *Connection) GlobalPrev(global string, ind *string, subs ...interface{}) (hasNext bool, err error) {
	msg := NewMessage(GLOBAL_ORDER)
	msg.Set(global)
	msg.Set(len(subs) + 1)
	for _, sub := range subs {
		msg.Set(sub)
	}
	msg.Set(*ind)
	msg.Set(7)

	if _, err = c.conn.Write(msg.Dump(c.count())); err != nil {
		return
	}

	if msg, err = ReadMessage(c.conn); err != nil {
		return
	}

	var result string
	msg.Get(&result)
	*ind = result
	hasNext = result != ""

	return
}

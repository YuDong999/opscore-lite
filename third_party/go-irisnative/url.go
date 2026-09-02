package intersystems

import (
	"fmt"
	"net"
	nurl "net/url"
	"sort"
	"strings"
)

// ParseURL no longer needs to be used by clients of this library since supplying a URL as a
// connection string to sql.Open() is now supported:
//
//	sql.Open("intersystems", "iris://_system:SYS@1.2.3.4:1972/USER")
//
// It remains exported here for backwards-compatibility.
//
// ParseURL converts a url to a connection string for driver.Open.
// Example:
//
//	"iris://_system:SYS@1.2.3.4:1972/USER"
//
// converts to:
//
//	"user=_system password=SYS host=1.2.3.4 port=1972 namespace=USER"
//
// A minimal example:
//
//	"iris://"
//
// This will be blank, causing driver.Open to use all of the defaults
func ParseURL(url string) (string, error) {
	u, err := nurl.Parse(url)
	if err != nil {
		return "", err
	}

	if u.Scheme != "iris" && u.Scheme != "IRIS" {
		return "", fmt.Errorf("invalid connection protocol: %s", u.Scheme)
	}

	var kvs []string
	escaper := strings.NewReplacer(`'`, `\'`, `\`, `\\`)
	accrue := func(k, v string) {
		if v != "" {
			kvs = append(kvs, k+"='"+escaper.Replace(v)+"'")
		}
	}

	if u.User != nil {
		v := u.User.Username()
		accrue("user", v)

		v, _ = u.User.Password()
		accrue("password", v)
	}

	if host, port, err := net.SplitHostPort(u.Host); err != nil {
		accrue("host", u.Host)
	} else {
		accrue("host", host)
		accrue("port", port)
	}

	if u.Path != "" {
		accrue("namespace", strings.ToUpper(u.Path[1:]))
	}

	q := u.Query()
	for k := range q {
		accrue(k, q.Get(k))
	}

	sort.Strings(kvs) // Makes testing easier (not a performance concern)
	return strings.Join(kvs, " "), nil
}

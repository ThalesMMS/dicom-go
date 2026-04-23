package ul

import "net"

type Association struct {
	Conn           net.Conn
	CalledAETitle  string
	CallingAETitle string
	Contexts       []PresentationContext
}

type DialOptions struct {
	CalledAETitle  string
	CallingAETitle string
	Contexts       []PresentationContext
}

func Dial(address string, opts DialOptions) (*Association, error) {
	conn, err := net.Dial("tcp", address)
	if err != nil {
		return nil, err
	}
	return &Association{Conn: conn, CalledAETitle: opts.CalledAETitle, CallingAETitle: opts.CallingAETitle, Contexts: opts.Contexts}, nil
}

func (a *Association) Close() error {
	if a == nil || a.Conn == nil {
		return nil
	}
	return a.Conn.Close()
}

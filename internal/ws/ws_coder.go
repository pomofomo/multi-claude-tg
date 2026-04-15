package ws

import (
	"context"
	"net/http"

	cws "github.com/coder/websocket"
)

// coderConn adapts a *cws.Conn to our minimal wsWriter interface.
type coderConn struct{ c *cws.Conn }

func (c coderConn) Write(ctx context.Context, typ int, data []byte) error {
	return c.c.Write(ctx, cws.MessageType(typ), data)
}

func (c coderConn) Close(code int, reason string) error {
	return c.c.Close(cws.StatusCode(code), reason)
}

// upgradeAndServeCoder does the WS handshake and runs the read loop.
func upgradeAndServeCoder(w http.ResponseWriter, r *http.Request, s *Server) error {
	c, err := cws.Accept(w, r, &cws.AcceptOptions{
		// Origin is not meaningful for a localhost-only channel server.
		InsecureSkipVerify: true,
	})
	if err != nil {
		return err
	}
	defer c.CloseNow()

	ctx := r.Context()
	conn := &Conn{ws: coderConn{c: c}, done: make(chan struct{})}
	instanceID := instanceIDFromCtx(ctx)

	read := func(ctx context.Context) ([]byte, error) {
		_, data, err := c.Read(ctx)
		return data, err
	}
	s.serveConn(ctx, conn, instanceID, read)
	return nil
}

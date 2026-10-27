package docker

import (
	"context"
	"io"
	"net"
	"os/exec"
	"sync"
	"time"
)

// cmdDialer returns a dialer whose connections are the stdin/stdout of a
// helper process (e.g. `wsl.exe -e docker system dial-stdio`).
func cmdDialer(name string, args ...string) func(ctx context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		cmd := exec.Command(name, args...)
		hideWindow(cmd)
		in, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return &cmdConn{cmd: cmd, in: in, out: out}, nil
	}
}

type cmdConn struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  io.ReadCloser
	once sync.Once
}

func (c *cmdConn) Read(b []byte) (int, error)  { return c.out.Read(b) }
func (c *cmdConn) Write(b []byte) (int, error) { return c.in.Write(b) }
func (c *cmdConn) Close() error {
	c.once.Do(func() {
		_ = c.in.Close()
		_ = c.out.Close()
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		_ = c.cmd.Wait()
	})
	return nil
}

type dummyAddr string

func (a dummyAddr) Network() string { return string(a) }
func (a dummyAddr) String() string  { return string(a) }

func (c *cmdConn) LocalAddr() net.Addr                { return dummyAddr("stdio") }
func (c *cmdConn) RemoteAddr() net.Addr               { return dummyAddr("stdio") }
func (c *cmdConn) SetDeadline(t time.Time) error      { return nil }
func (c *cmdConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *cmdConn) SetWriteDeadline(t time.Time) error { return nil }

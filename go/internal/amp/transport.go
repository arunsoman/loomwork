package amp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"loomwork.dev/loomwork/internal/aci"
)

// Transport is the interface for AMP transports.
type Transport interface {
	Send(env *Envelope) error
	// Recv blocks until a message arrives, the context ends, or the stream closes.
	Recv(ctx context.Context) (*Envelope, error)
	Close() error
}

type line struct {
	data []byte
	err  error
}

// LineTransport carries AMP as newline-delimited JSON over a reader/writer
// pair. One goroutine owns the reader, so a timed-out Recv does not leak a
// goroutine or lose data; lines longer than MaxMessageBytes are rejected.
type LineTransport struct {
	w      io.Writer
	closer func() error
	lines  chan line
	mu     sync.Mutex
}

// NewLineTransport wraps r and w.
func NewLineTransport(r io.Reader, w io.Writer, closer func() error) *LineTransport {
	t := &LineTransport{w: w, closer: closer, lines: make(chan line)}
	go func() {
		defer close(t.lines)
		br := bufio.NewReaderSize(r, 64<<10)
		for {
			var buf []byte
			for {
				chunk, isPrefix, err := br.ReadLine()
				if err != nil {
					if len(buf) == 0 {
						t.lines <- line{err: err}
						return
					}
					break
				}
				buf = append(buf, chunk...)
				if len(buf) > MaxMessageBytes {
					t.lines <- line{err: fmt.Errorf("PROTOCOL_VIOLATION: message exceeds %d bytes", MaxMessageBytes)}
					return
				}
				if !isPrefix {
					break
				}
			}
			if len(buf) == 0 {
				continue
			}
			t.lines <- line{data: buf}
		}
	}()
	return t
}

// NewStdioTransport wraps stdin/stdout.
func NewStdioTransport() *LineTransport {
	return NewLineTransport(os.Stdin, os.Stdout, nil)
}

// NewExecTransport starts cmd and talks to it over its stdin/stdout.
func NewExecTransport(cmd *exec.Cmd) (*LineTransport, error) {
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return NewLineTransport(out, in, func() error {
		in.Close()
		return cmd.Wait()
	}), nil
}

// Send writes one message followed by a newline.
func (t *LineTransport) Send(env *Envelope) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	data, err := env.Marshal()
	if err != nil {
		return err
	}
	if len(data) > MaxMessageBytes {
		return fmt.Errorf("message exceeds %d bytes", MaxMessageBytes)
	}
	_, err = t.w.Write(append(data, '\n'))
	return err
}

// Recv reads the next message.
func (t *LineTransport) Recv(ctx context.Context) (*Envelope, error) {
	select {
	case l, ok := <-t.lines:
		if !ok {
			return nil, io.EOF
		}
		if l.err != nil {
			return nil, l.err
		}
		return UnmarshalEnvelope(l.data)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close closes the underlying stream, if it has a closer.
func (t *LineTransport) Close() error {
	if t.closer != nil {
		return t.closer()
	}
	return nil
}

// Client is a thin AMP client: Delegate and Report.
type Client struct {
	transport Transport
	timeout   time.Duration
}

// NewClient constructs a client.
func NewClient(t Transport) *Client {
	return &Client{transport: t, timeout: 30 * time.Second}
}

// SetTimeout changes the per-response timeout.
func (c *Client) SetTimeout(d time.Duration) { c.timeout = d }

func (c *Client) recv() (*Envelope, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	return c.transport.Recv(ctx)
}

// Delegate sends an amp/delegate request carrying the given capability tokens
// and waits for the response.
func (c *Client) Delegate(params *DelegateParams, prov *Provenance, tokens []CapabilityToken) (*DelegateResult, error) {
	req, err := NewRequest("amp/delegate", params, prov)
	if err != nil {
		return nil, err
	}
	if tokens != nil {
		req.Capabilities = tokens
	}
	if err := c.transport.Send(req); err != nil {
		return nil, err
	}
	resp, err := c.recv()
	if err != nil {
		return nil, err
	}
	if resp.ID != req.ID {
		return nil, fmt.Errorf("response id %q does not match request %q", resp.ID, req.ID)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("amp/delegate error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	var result DelegateResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// AwaitReport waits for the peer's amp/report request for an accepted task and
// acknowledges it.
func (c *Client) AwaitReport(timeout time.Duration) (*ReportParams, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	env, err := c.transport.Recv(ctx)
	if err != nil {
		return nil, err
	}
	if env.Method != "amp/report" {
		return nil, fmt.Errorf("expected amp/report, got %q", env.Method)
	}
	var rp ReportParams
	if err := json.Unmarshal(env.Params, &rp); err != nil {
		return nil, err
	}
	ack, _ := NewResponse(env.ID, map[string]bool{"received": true}, env.AMP.TraceID)
	if err := c.transport.Send(ack); err != nil {
		return nil, err
	}
	return &rp, nil
}

// Report sends an amp/report request and returns whether it was acknowledged.
func (c *Client) Report(params *ReportParams, prov *Provenance) (bool, error) {
	req, err := NewRequest("amp/report", params, prov)
	if err != nil {
		return false, err
	}
	if err := c.transport.Send(req); err != nil {
		return false, err
	}
	resp, err := c.recv()
	if err != nil {
		return false, err
	}
	if resp.Error != nil {
		return false, fmt.Errorf("amp/report error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	var ack struct {
		Received bool `json:"received"`
	}
	if err := json.Unmarshal(resp.Result, &ack); err != nil {
		return false, err
	}
	return ack.Received, nil
}

// TaskHandler runs a delegated task and returns its text result.
type TaskHandler func(spec DelegateSpec) (string, error)

// Server answers AMP requests on a transport.
type Server struct {
	Transport Transport
	// Trusted are the keys allowed to issue capability tokens and to sign the
	// provenance of callers.
	Trusted []*aci.VerifyingKey
	// Skills lists the skill names this agent offers.
	Skills  []string
	Handler TaskHandler
	Now     func() time.Time
}

// Serve handles requests until the peer closes the stream or ctx ends.
func (s *Server) Serve(ctx context.Context) error {
	if s.Now == nil {
		s.Now = time.Now
	}
	for {
		env, err := s.Transport.Recv(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
				return nil
			}
			// A malformed message is answered with an error; a broken stream ends.
			if isProtocolViolation(err) {
				_ = s.Transport.Send(NewError("", CodeProtocolViolation, err.Error(), ""))
				continue
			}
			return err
		}
		if env.Method == "" {
			continue // a stray response
		}
		switch env.Method {
		case "amp/delegate":
			s.handleDelegate(env)
		case "amp/report":
			ack, _ := NewResponse(env.ID, map[string]bool{"received": true}, env.AMP.TraceID)
			_ = s.Transport.Send(ack)
		default:
			_ = s.Transport.Send(NewError(env.ID, CodeMethodNotFound, "unknown method "+env.Method, env.AMP.TraceID))
		}
	}
}

func isProtocolViolation(err error) bool {
	return err != nil && len(err.Error()) >= 18 && err.Error()[:18] == "PROTOCOL_VIOLATION"
}

func (s *Server) deny(env *Envelope, code int, msg string) {
	_ = s.Transport.Send(NewError(env.ID, code, msg, env.AMP.TraceID))
}

func (s *Server) handleDelegate(env *Envelope) {
	if env.AMP.Version != "0.1" {
		s.deny(env, CodeVersionMismatch, "unsupported AMP version "+env.AMP.Version)
		return
	}
	if _, err := VerifyProvenance(env.Provenance, s.Trusted); err != nil {
		s.deny(env, CodeUnauthenticated, err.Error())
		return
	}
	var p DelegateParams
	if err := json.Unmarshal(env.Params, &p); err != nil || p.TaskID == "" {
		s.deny(env, CodeProtocolViolation, "invalid amp/delegate params")
		return
	}
	skill := p.Spec.Skill
	if skill == "" {
		skill = "answer_question"
	}
	offered := false
	for _, k := range s.Skills {
		if k == skill {
			offered = true
		}
	}
	if !offered {
		s.deny(env, CodeTaskNotFound, "this agent does not offer skill "+skill)
		return
	}
	var lastErr error = fmt.Errorf("no capability token presented")
	authorized := false
	for i := range env.Capabilities {
		if lastErr = VerifyCapability(&env.Capabilities[i], skill, s.Trusted, s.Now()); lastErr == nil {
			authorized = true
			break
		}
	}
	if !authorized {
		s.deny(env, CodeCapabilityDenied, "capability denied: "+lastErr.Error())
		return
	}

	p.Spec.Skill = skill
	out, err := s.Handler(p.Spec)
	resp, respErr := NewResponse(env.ID, DelegateResult{Accepted: err == nil, Reason: errString(err)}, env.AMP.TraceID)
	if respErr != nil {
		s.deny(env, CodeProtocolViolation, "could not build response: "+respErr.Error())
		return
	}
	if err := s.Transport.Send(resp); err != nil {
		return
	}

	// Report the outcome back to the caller. Delivery is fire-and-forget in
	// v0.1: the caller's acknowledgement arrives as an ordinary response
	// message, which Serve ignores.
	rp := ReportParams{TaskID: p.TaskID, Status: "complete"}
	if err != nil {
		rp.Status, rp.Error = "failed", err.Error()
	} else {
		rp.Artifacts = []ReportArtifact{{Name: "answer.txt", Mime: "text/plain", Digest: aci.Sha256Bytes([]byte(out)), Inline: out}}
	}
	req, err2 := NewRequest("amp/report", rp, env.Provenance)
	if err2 != nil {
		return
	}
	req.AMP.TraceID = env.AMP.TraceID
	_ = s.Transport.Send(req)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

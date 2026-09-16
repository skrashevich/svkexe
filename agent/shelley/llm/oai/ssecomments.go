package oai

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"strings"
)

// Server-sent events reserve lines starting with ':' for comments, and
// providers use them to keep a slow response alive: OpenRouter sends
// ": OPENROUTER PROCESSING" while a model is still queued, and its docs tell
// clients to skip those lines. go-openai does not — it counts every line that
// is not a data frame as an empty message and gives up after 300 in a row with
// "stream has sent too many empty messages". A model that pauses long enough
// mid-answer therefore kills the turn, which is exactly what a queued free
// model does.
//
// Strip the comments before the parser sees them. They carry no content, so
// dropping them changes nothing except that a keep-alive no longer counts
// against a limit meant for junk. Real non-data noise still reaches the parser
// and still trips that limit.
func withoutSSEComments(c *http.Client) *http.Client {
	stripped := *c
	stripped.Transport = sseCommentStripper{base: c.Transport}
	return &stripped
}

type sseCommentStripper struct{ base http.RoundTripper }

func (t sseCommentStripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if err != nil || resp.Body == nil {
		return resp, err
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return resp, nil
	}
	resp.Body = stripSSEComments(resp.Body)
	return resp, nil
}

// stripSSEComments forwards the body line by line, dropping comment lines. It
// must not buffer beyond one line: the caller is streaming a response to a
// user as it arrives.
func stripSSEComments(body io.ReadCloser) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		reader := bufio.NewReader(body)
		// A comment is an event like any other and ends with a blank line.
		// Dropping the comment but keeping its terminator would leave the very
		// empty line the parser objects to, so the pair goes together.
		droppedComment := false
		for {
			line, err := reader.ReadBytes('\n')
			trimmed := bytes.TrimRight(line, "\r\n")
			switch {
			case bytes.HasPrefix(bytes.TrimLeft(trimmed, " \t"), []byte(":")):
				droppedComment = true
			case droppedComment && len(trimmed) == 0:
				droppedComment = false
			case len(line) > 0:
				droppedComment = false
				if _, writeErr := pw.Write(line); writeErr != nil {
					// The reader is gone; stop pulling from the upstream body.
					pw.CloseWithError(writeErr)
					return
				}
			}
			if err != nil {
				pw.CloseWithError(err)
				return
			}
		}
	}()
	return sseBody{Reader: pr, upstream: body}
}

// sseBody closes the upstream response so the connection is released even when
// the caller abandons the stream mid-answer.
type sseBody struct {
	io.Reader
	upstream io.ReadCloser
}

func (b sseBody) Close() error {
	err := b.upstream.Close()
	if pr, ok := b.Reader.(*io.PipeReader); ok {
		pr.CloseWithError(io.ErrUnexpectedEOF)
	}
	return err
}

package bridge

import "context"

// Streamer is the capability for bridges that can stream a message as
// incremental drafts and then persist it. Telegram implements this via
// sendMessageDraft + sendMessage.
type Streamer interface {
	OpenStream(ctx context.Context) (MessageStream, error)
}

// MessageStream is one in-flight outbound stream.
type MessageStream interface {
	Write(p []byte) (int, error)
	Close() error
	// Done is closed when the stream ends, including self-close on abandon.
	Done() <-chan struct{}
}

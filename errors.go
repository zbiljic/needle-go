package needle

import "errors"

// ErrClosed is returned by an Agent or Whistle after Close.
var ErrClosed = errors.New("needle: closed")

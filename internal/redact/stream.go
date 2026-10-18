package redact

// ctxKeep is how many consumed input bytes a Stream keeps for look-behind
// (the 8 digits before a Telegram token's ':').
const ctxKeep = 8

// pemTail is the longest END line a dropped PEM block may still be inside:
// "-----END " + 64-byte label + "-----".
const pemTail = 78

// Stream is the streaming redaction filter (SPEC §14.3, ADR-081 butir 2).
// It holds back only the shortest suffix that may still become a secret:
// from a (possibly partial) pattern literal such as "sk-", "gh", "://" or
// "-----BEGIN", from the ':' after ≥ 8 digits (Telegram), or from a partial
// known secret; all other bytes are emitted by the Push that delivers them.
// An unbounded token tail is held to its terminator or the 4 KiB cap, after
// which it is redacted fail-closed and its remainder dropped; a PEM private
// key block is held until its END line, the cap or Flush.
//
// For every chunking, the concatenated output equals [Redactor.Redact] of
// the concatenated input unless a hold reached the cap. A Stream is not safe
// for concurrent use; it is reusable after Flush.
type Stream struct {
	r    *Redactor
	drop *pattern // non-nil: dropping the tail of a token cut fail-closed
	buf  []byte   // buf[:ctx] is consumed input kept for look-behind; the rest is pending
	ctx  int
}

// NewStream returns a streaming filter using r.
func (r *Redactor) NewStream() *Stream {
	return &Stream{r: r}
}

// Push feeds chunk and appends the output that became final to dst.
func (s *Stream) Push(dst, chunk []byte) []byte {
	s.buf = append(s.buf, chunk...)
	return s.process(dst, modeStream)
}

// Flush ends the stream: held bytes are decided as at end of input (an
// unterminated PEM block is redacted whole) and appended to dst.
func (s *Stream) Flush(dst []byte) []byte {
	dst = s.process(dst, modeFinal)
	s.buf, s.ctx, s.drop = s.buf[:0], 0, nil
	return dst
}

func (s *Stream) process(dst []byte, md mode) []byte {
	if s.drop != nil && !s.dropTail(md) {
		return dst
	}
	out, last, safe, cut := s.r.scan(dst, s.buf, s.ctx, md)
	out = append(out, s.buf[last:safe]...)
	s.drop = cut
	s.consume(safe)
	return out
}

// dropTail discards pending bytes that continue a cut token and reports
// whether the token ended.
func (s *Stream) dropTail(md mode) bool {
	if s.drop.pem {
		end, st := pemEnd(s.buf, s.ctx, s.drop.drop, md)
		if st == stMore {
			s.consume(max(s.ctx, len(s.buf)-pemTail))
			return false
		}
		s.consume(end)
		s.drop = nil
		return true
	}
	k := s.ctx
	for k < len(s.buf) && s.drop.drop[s.buf[k]] {
		k++
	}
	open := k == len(s.buf) && md == modeStream
	s.consume(k)
	if open {
		return false
	}
	s.drop = nil
	return true
}

// consume marks buf[:p] as consumed, keeping its last ctxKeep bytes.
func (s *Stream) consume(p int) {
	keep := max(0, p-ctxKeep)
	n := copy(s.buf, s.buf[keep:])
	s.buf = s.buf[:n]
	s.ctx = p - keep
}

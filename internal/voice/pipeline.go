package voice

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Pipeline failures.
var (
	ErrPipelineNotConfigured = errors.New("voice: pipeline is not configured")
	ErrEmptyTranscript       = errors.New("voice: transcript is empty")
	ErrEmptyReply            = errors.New("voice: reply is empty")
)

// LLM streams one reply for one transcript; each complete sentence becomes one
// TTS request, so audio starts while the reply is still being generated.
type LLM interface {
	Stream(ctx context.Context, prompt string, emit func(delta string) error) error
}

// Result is one completed voice turn.
type Result struct {
	Transcript Transcript
	Reply      string
	Audio      []Audio
}

// Pipeline is the minimal voice mode: STT → LLM → sentence-streamed TTS.
type Pipeline struct {
	STT   STT
	TTS   TTS
	LLM   LLM
	Synth SynthOptions
}

// Turn transcribes audio, streams the reply, and synthesizes each sentence.
func (p *Pipeline) Turn(ctx context.Context, audio io.Reader, opts TranscriptOptions) (Result, error) {
	if p == nil || p.STT == nil || p.TTS == nil || p.LLM == nil || audio == nil {
		return Result{}, ErrPipelineNotConfigured
	}
	tr, err := p.STT.Transcribe(ctx, audio, opts)
	if err != nil {
		return Result{}, fmt.Errorf("voice: transcribe: %w", err)
	}
	if strings.TrimSpace(tr.Text) == "" {
		return Result{}, ErrEmptyTranscript
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sentences := make(chan string, 8)
	streamErr := make(chan error, 1)
	go func() {
		defer close(sentences)
		streamErr <- p.stream(runCtx, tr.Text, sentences)
	}()
	res := Result{Transcript: tr}
	var reply strings.Builder
	for s := range sentences {
		reply.WriteString(s)
		reply.WriteByte(' ')
		clip, err := p.TTS.Synthesize(runCtx, s, p.Synth)
		if err != nil {
			cancel()
			drain(sentences)
			<-streamErr
			return Result{}, fmt.Errorf("voice: synthesize: %w", err)
		}
		res.Audio = append(res.Audio, clip)
	}
	if err := <-streamErr; err != nil {
		return Result{}, fmt.Errorf("voice: llm: %w", err)
	}
	if reply.Len() == 0 {
		return Result{}, ErrEmptyReply
	}
	res.Reply = strings.TrimSpace(reply.String())
	return res, nil
}

// stream turns LLM deltas into sentences and stops on the first delivery error.
func (p *Pipeline) stream(ctx context.Context, prompt string, out chan<- string) error {
	var buf strings.Builder
	emit := func(text string) error {
		if text = strings.TrimSpace(text); text == "" {
			return nil
		}
		select {
		case out <- text:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	err := p.LLM.Stream(ctx, prompt, func(delta string) error {
		buf.WriteString(delta)
		for {
			head, rest, ok := splitSentence(buf.String())
			if !ok {
				return nil
			}
			buf.Reset()
			buf.WriteString(rest)
			if err := emit(head); err != nil {
				return err
			}
		}
	})
	if err != nil {
		return err
	}
	return emit(buf.String())
}

// drain consumes the sentence channel after an abort so the producer finishes.
func drain(ch chan string) {
	for range ch {
	}
}

// splitSentence cuts one sentence at ., !, ?, …, or a newline that ends a word.
func splitSentence(s string) (head, rest string, ok bool) {
	for i, r := range s {
		if !strings.ContainsRune(".!?…\n", r) {
			continue
		}
		j := i + utf8.RuneLen(r)
		if j < len(s) && r != '\n' && s[j] != ' ' && s[j] != '\t' {
			continue
		}
		head = strings.TrimSpace(s[:j])
		if head == "" {
			continue
		}
		return head, strings.TrimLeft(s[j:], " \t\r\n"), true
	}
	return "", s, false
}

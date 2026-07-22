package openai

import sdkopenai "github.com/sashabaranov/go-openai"

type chatCompletionStream struct {
	stream *sdkopenai.ChatCompletionStream
}

func (s chatCompletionStream) Recv() (sdkopenai.ChatCompletionStreamResponse, error) {
	return s.stream.Recv()
}

func (s chatCompletionStream) Close() error {
	return s.stream.Close()
}

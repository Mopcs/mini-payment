package observability

// AMQPHeaders adapts AMQP message headers to the OpenTelemetry text map carrier,
// so trace context travels with the message instead of in the event payload.
type AMQPHeaders map[string]any

func (h AMQPHeaders) Get(key string) string {
	value, ok := h[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

func (h AMQPHeaders) Set(key, value string) {
	h[key] = value
}

func (h AMQPHeaders) Keys() []string {
	keys := make([]string, 0, len(h))
	for key := range h {
		keys = append(keys, key)
	}
	return keys
}

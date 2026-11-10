package meter

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`
	CachedTokens     int64 `json:"cached_tokens"`
}

type Snapshot struct {
	Seq         int64
	LocalOut    int64
	ProviderOut int64
	HasOut      bool
	Prompt      int64
	HasPrompt   bool
}

type ToolCall struct {
	Name string
	Args strings.Builder
}

type Result struct {
	InputTokens       int64
	OutputTokens      int64
	ReasoningTokens   int64
	CachedInputTokens int64
	Source            string
	Trust             string
	SnapshotSeq       int64
	LocalAtSnapshot   int64
	LocalOutputTokens int64
	Detail            map[string]any
}

type Accumulator struct {
	mu        sync.Mutex
	counter   Tokenizer
	localIn   int64
	maxOut    int64
	seq       int64
	content   strings.Builder
	reasoning strings.Builder
	tools     map[int]*ToolCall
	usage     Usage
	hasPrompt bool
	hasUsage  bool
	snap      Snapshot
	localOut  int64
	dirty     bool
	approx    int64
	finish    string
	startedAt time.Time
	firstAt   time.Time
	lastAt    time.Time
}

func NewAccumulator(counter Tokenizer, localInput, maxOut int64) *Accumulator {
	now := time.Now()
	return &Accumulator{
		counter:   counter,
		localIn:   localInput,
		maxOut:    maxOut,
		tools:     map[int]*ToolCall{},
		startedAt: now,
		lastAt:    now,
	}
}

func (a *Accumulator) Content() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.content.String()
}

func (a *Accumulator) AddContent(s string) bool {
	if s == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.content.WriteString(s)
	a.dirty = true
	a.approx += a.counter.CountText(s)
	first := a.firstAt.IsZero()
	if first {
		a.firstAt = time.Now()
	}
	a.lastAt = time.Now()
	return first
}

func (a *Accumulator) AddReasoning(s string) {
	if s == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reasoning.WriteString(s)
	a.dirty = true
	a.approx += a.counter.CountText(s)
	if a.firstAt.IsZero() {
		a.firstAt = time.Now()
	}
	a.lastAt = time.Now()
}

func (a *Accumulator) AddToolCall(index int, name, argsDelta string) {
	if name == "" && argsDelta == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	tc, ok := a.tools[index]
	if !ok {
		tc = &ToolCall{}
		a.tools[index] = tc
	}
	if name != "" {
		if tc.Name == "" {
			tc.Name = name
		} else if !strings.HasSuffix(tc.Name, name) {
			tc.Name += name
		}
	}
	if argsDelta != "" {
		tc.Args.WriteString(argsDelta)
		a.approx += a.counter.CountText(argsDelta)
	}
	a.dirty = true
	if a.firstAt.IsZero() {
		a.firstAt = time.Now()
	}
	a.lastAt = time.Now()
}

func (a *Accumulator) OnUsage(u Usage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.hasUsage = true
	if u.PromptTokens > 0 {
		a.usage.PromptTokens = u.PromptTokens
		a.hasPrompt = true
	}
	if u.CompletionTokens > 0 {
		a.usage.CompletionTokens = u.CompletionTokens
	} else if u.TotalTokens > u.PromptTokens && a.usage.CompletionTokens == 0 {
		// Some gateways report totals only; derive conservatively.
		a.usage.CompletionTokens = u.TotalTokens - u.PromptTokens
	}
	if u.ReasoningTokens > 0 {
		a.usage.ReasoningTokens = u.ReasoningTokens
	}
	if u.CachedTokens > 0 {
		a.usage.CachedTokens = u.CachedTokens
	}
	a.seq++
	snap := Snapshot{Seq: a.seq, LocalOut: a.localOutputLocked()}
	if a.hasPrompt {
		snap.Prompt, snap.HasPrompt = a.usage.PromptTokens, true
	}
	if a.usage.CompletionTokens > 0 {
		snap.ProviderOut, snap.HasOut = a.usage.CompletionTokens, true
	}
	a.snap = snap
}

func (a *Accumulator) OnFinish(reason string) {
	if reason == "" {
		return
	}
	a.mu.Lock()
	a.finish = reason
	a.mu.Unlock()
}

func (a *Accumulator) NextSeq() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seq++
	return a.seq
}

func (a *Accumulator) localOutputLocked() int64 {
	if !a.dirty {
		return a.localOut
	}
	tools := make([]ToolText, 0, len(a.tools))
	for _, tc := range a.tools {
		tools = append(tools, ToolText{Name: tc.Name, Args: tc.Args.String()})
	}
	a.localOut = a.counter.CountOutput(a.content.String(), a.reasoning.String(), tools)
	a.dirty = false
	return a.localOut
}

func (a *Accumulator) LocalOutput() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.localOutputLocked()
}

func (a *Accumulator) ApproxOutput() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.approx > a.localOut {
		return a.approx
	}
	return a.localOut
}

func (a *Accumulator) HasUsage() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hasUsage
}

func (a *Accumulator) Snapshot() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.snap
}

func (a *Accumulator) FirstTokenAt() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.firstAt
}

func (a *Accumulator) LastChunkAt() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastAt
}

func (a *Accumulator) Finalize(kOut, kIn float64) Result {
	a.mu.Lock()
	defer a.mu.Unlock()
	localOut := a.localOutputLocked()
	res := Result{
		InputTokens:       Calibrate(a.localIn, kIn),
		OutputTokens:      Calibrate(localOut, kOut),
		ReasoningTokens:   a.usage.ReasoningTokens,
		CachedInputTokens: a.usage.CachedTokens,
		Source:            "counted_text",
		Trust:             "local_estimate",
		SnapshotSeq:       a.snap.Seq,
		LocalAtSnapshot:   a.snap.LocalOut,
		LocalOutputTokens: localOut,
		Detail: map[string]any{
			"content_chars":   a.content.Len(),
			"reasoning_chars": a.reasoning.Len(),
			"tool_calls":      len(a.tools),
			"finish_reason":   a.finish,
			"usage_seen":      a.hasUsage,
			"cached_input":    a.usage.CachedTokens,
			"local_output":    localOut,
		},
	}

	switch {
	case a.usage.CompletionTokens > 0:
		res.OutputTokens = a.usage.CompletionTokens
		res.Source = "provider_final"
		res.Trust = "reported"
		res.Detail["provider_output"] = a.usage.CompletionTokens
		if a.hasPrompt {
			res.InputTokens = a.usage.PromptTokens
		}
	case a.snap.HasOut:
		delta := localOut - a.snap.LocalOut
		if delta < 0 {
			delta = 0
		}
		tail := int64(0)
		if delta > 0 {
			tail = Calibrate(delta, kOut)
		}
		if a.maxOut > 0 && tail > a.maxOut {
			tail = a.maxOut
		}
		res.OutputTokens = a.snap.ProviderOut + tail
		res.Source = "provider_partial"
		res.Trust = "reported"
		res.Detail["snapshot_provider_output"] = a.snap.ProviderOut
		res.Detail["tail_local"] = delta
		res.Detail["tail_calibrated"] = tail
		if a.snap.HasPrompt {
			res.InputTokens = a.snap.Prompt
		}
	default:
		if localOut == 0 && a.localIn > 0 {
			res.Source = "estimate_prompt_only"
			res.Trust = "local_estimate"
			res.OutputTokens = 0
		}
	}

	if res.InputTokens == 0 && res.OutputTokens == 0 {
		res.Source = "none"
		res.Trust = "none"
	}
	return res
}

func (r Result) DetailJSON() string {
	b, err := json.Marshal(r.Detail)
	if err != nil {
		return "{}"
	}
	return string(b)
}

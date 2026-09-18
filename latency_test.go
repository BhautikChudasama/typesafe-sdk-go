//go:build integration

package typesafe_test

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	typesafe "github.com/Tangerg/typesafe-sdk-go"
)

// The latency probe measures the service and this SDK's share of it. It is
// behind an env var as well as the integration tag, because it makes a few
// hundred requests and the rest of the integration suite should not.
//
//	TYPESAFE_API_KEY=... TYPESAFE_LATENCY=1 go test -tags integration -run TestLatency -v .
//
// Every number it prints is one run against one network path from one place.
// Treat it as a measurement of this client's overhead, not as a benchmark of
// the service.

func latencyEnabled(t *testing.T) {
	t.Helper()
	if os.Getenv("TYPESAFE_LATENCY") == "" {
		t.Skip("set TYPESAFE_LATENCY=1 to run the latency probe")
	}
}

// A sample is one call: how long it took end to end, and how long the service
// said it spent upstream. The difference is network plus this SDK.
type sample struct {
	total    time.Duration
	upstream time.Duration
}

type samples []sample

func (s samples) totals() []time.Duration {
	out := make([]time.Duration, len(s))
	for i, one := range s {
		out[i] = one.total
	}
	slices.Sort(out)
	return out
}

func (s samples) upstreams() []time.Duration {
	out := make([]time.Duration, 0, len(s))
	for _, one := range s {
		if one.upstream > 0 {
			out = append(out, one.upstream)
		}
	}
	slices.Sort(out)
	return out
}

func (s samples) overheads() []time.Duration {
	out := make([]time.Duration, 0, len(s))
	for _, one := range s {
		if one.upstream > 0 {
			out = append(out, one.total-one.upstream)
		}
	}
	slices.Sort(out)
	return out
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(p * float64(len(sorted)-1))
	return sorted[i]
}

func describe(label string, s samples) string {
	totals := s.totals()
	overheads := s.overheads()
	line := fmt.Sprintf("%-28s n=%-3d  min %6s  p50 %6s  p90 %6s  p99 %6s  max %6s",
		label, len(totals),
		round(percentile(totals, 0)), round(percentile(totals, 0.50)),
		round(percentile(totals, 0.90)), round(percentile(totals, 0.99)),
		round(percentile(totals, 1)))
	if upstreams := s.upstreams(); len(upstreams) > 0 {
		line += fmt.Sprintf("  || service p50 %6s  round-trip p50 %6s",
			round(percentile(upstreams, 0.50)), round(percentile(overheads, 0.50)))
	}
	return line
}

func round(d time.Duration) time.Duration { return d.Round(time.Millisecond) }

// upstreamTime reads the time the service spent on the request, which the
// gateway reports in milliseconds.
func upstreamTime(meta typesafe.Meta) time.Duration {
	raw := meta.Header.Get("X-Envoy-Upstream-Service-Time")
	ms, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

// measure calls f n times in sequence and collects what each one cost.
func measure(t *testing.T, n int, f func() (typesafe.Meta, error)) samples {
	t.Helper()
	out := make(samples, 0, n)
	for range n {
		started := time.Now()
		meta, err := f()
		took := time.Since(started)
		if err != nil {
			t.Fatalf("call failed: %v", err)
		}
		out = append(out, sample{total: took, upstream: upstreamTime(meta)})
	}
	return out
}

func nouls(count int) typesafe.Questions {
	questions := make(typesafe.Questions, count)
	for i := range count {
		questions[fmt.Sprintf("q%d", i)] = &typesafe.NoulQuestion{
			Instructions: fmt.Sprintf("Is the customer unhappy? (phrasing %d)", i),
		}
	}
	return questions
}

const shortTicket = "I was charged twice this month and I am not happy about it."

// TestLatencySingleQuestion is the shape closest to the number on the marketing
// page: one question, a short state.
func TestLatencySingleQuestion(t *testing.T) {
	latencyEnabled(t)
	client := liveClient(t, nil)
	ask := func() (typesafe.Meta, error) {
		result, err := client.SystemOne(liveContext(t), &typesafe.SystemOneRequest{
			State:     shortTicket,
			Questions: nouls(1),
		}, nil)
		if err != nil {
			return typesafe.Meta{}, err
		}
		return result.Meta, nil
	}

	measure(t, 3, ask) // warm the connection and TLS session first
	t.Log(describe("1 noul, short state", measure(t, 30, ask)))
}

// TestLatencyByQuestionCount tests the claim that questions are evaluated in
// parallel. If they are, latency should be close to flat as the count rises;
// if they were evaluated in turn, it would climb with it.
func TestLatencyByQuestionCount(t *testing.T) {
	latencyEnabled(t)
	client := liveClient(t, nil)

	for _, count := range []int{1, 2, 4, 8, 16} {
		questions := nouls(count)
		ask := func() (typesafe.Meta, error) {
			result, err := client.SystemOne(liveContext(t), &typesafe.SystemOneRequest{
				State:     shortTicket,
				Questions: questions,
			}, nil)
			if err != nil {
				return typesafe.Meta{}, err
			}
			return result.Meta, nil
		}
		measure(t, 2, ask)
		t.Log(describe(fmt.Sprintf("%d nouls", count), measure(t, 12, ask)))
	}
}

// TestLatencyByPrimitive checks whether the three question types cost the same.
func TestLatencyByPrimitive(t *testing.T) {
	latencyEnabled(t)
	client := liveClient(t, nil)

	cases := map[string]typesafe.Questions{
		"noul": {"q": &typesafe.NoulQuestion{Instructions: "Is the customer unhappy?"}},
		"choice": {"q": &typesafe.ChoiceQuestion{
			Instructions: "What is the tone?",
			Criteria:     typesafe.ChoiceCriteria{"calm": nil, "frustrated": nil, "angry": nil},
		}},
		"score": {"q": &typesafe.ScoreQuestion{
			Instructions: "How urgent?",
			Criteria:     typesafe.ScoreCriteria{"can wait", "this week", "today", "right now"},
		}},
	}
	for _, name := range slices.Sorted(maps.Keys(cases)) {
		questions := cases[name]
		ask := func() (typesafe.Meta, error) {
			result, err := client.SystemOne(liveContext(t), &typesafe.SystemOneRequest{
				State:     shortTicket,
				Questions: questions,
			}, nil)
			if err != nil {
				return typesafe.Meta{}, err
			}
			return result.Meta, nil
		}
		measure(t, 2, ask)
		t.Log(describe(name, measure(t, 12, ask)))
	}
}

// TestLatencyByStateSize measures how the answer time moves with the amount of
// text being judged.
func TestLatencyByStateSize(t *testing.T) {
	latencyEnabled(t)
	client := liveClient(t, nil)

	paragraph := "The customer reports being charged twice for the same order and " +
		"has asked for a refund; the account shows one subscription and two " +
		"settled payments on consecutive days. "
	for _, repeats := range []int{1, 8, 32} {
		state := strings.Repeat(paragraph, repeats)
		ask := func() (typesafe.Meta, error) {
			result, err := client.SystemOne(liveContext(t), &typesafe.SystemOneRequest{
				State:     state,
				Questions: nouls(1),
			}, nil)
			if err != nil {
				return typesafe.Meta{}, err
			}
			return result.Meta, nil
		}
		measure(t, 2, ask)
		t.Log(describe(fmt.Sprintf("state ~%d chars", len(state)), measure(t, 10, ask)))
	}
}

// TestLatencyUnderConcurrency runs the fan-out the documentation recommends and
// reports the throughput one shared client reaches.
func TestLatencyUnderConcurrency(t *testing.T) {
	latencyEnabled(t)
	client := liveClient(t, nil)

	for _, workers := range []int{1, 4, 16} {
		const perWorker = 6
		var mu sync.Mutex
		var collected samples

		started := time.Now()
		var wg sync.WaitGroup
		for range workers {
			wg.Go(func() {
				local := make(samples, 0, perWorker)
				for range perWorker {
					at := time.Now()
					result, err := client.SystemOne(liveContext(t), &typesafe.SystemOneRequest{
						State:     shortTicket,
						Questions: nouls(1),
					}, nil)
					if err != nil {
						t.Errorf("worker call failed: %v", err)
						return
					}
					local = append(local, sample{total: time.Since(at), upstream: upstreamTime(result.Meta)})
				}
				mu.Lock()
				collected = append(collected, local...)
				mu.Unlock()
			})
		}
		wg.Wait()
		wall := time.Since(started)

		calls := workers * perWorker
		t.Log(describe(fmt.Sprintf("%d workers", workers), collected) +
			fmt.Sprintf("   | %d calls in %s = %.1f req/s", calls, wall.Round(time.Millisecond),
				float64(calls)/wall.Seconds()))
	}
}

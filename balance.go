package connectix

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
)

// Balance is what the application can spend, per channel, as Connectix
// answered it (API 1.3.0). Every amount is Connectix's decimal string in EUR
// with four decimals: money is never a float here.
type Balance struct {
	// Currency is the currency of every amount; Connectix answers "EUR".
	Currency string `json:"currency"`
	// Total is the own funds of every listed channel plus each pool counted
	// once. It is a convenience figure: funds are spent per channel, so judge
	// a send by the channel's [ChannelBalance.Available].
	Total string `json:"total"`
	// Threshold is the low-balance alert threshold set for the company in the
	// console; nil when there is none.
	Threshold *string `json:"threshold"`
	// LowBalance is true when at least one channel is low.
	LowBalance bool `json:"lowBalance"`
	// Channels holds one entry per channel ("sms", "viber", "noti") the
	// application has an active service on; empty, never nil, when there is
	// none. A channel that is absent has no service: a send on it will not
	// be charged from this balance.
	Channels map[string]ChannelBalance `json:"channels"`
	// Pools are the shared top-up pools the listed channels draw from.
	Pools []BalancePool `json:"pools"`
	// RateLimitRemaining is the requests left in the current minute for the
	// application, from X-RateLimit-Remaining; -1 when the header is absent.
	RateLimitRemaining int `json:"-"`
	// Raw is the answer exactly as received.
	Raw json.RawMessage `json:"-"`
}

// ChannelBalance is the funds of the application on one channel.
type ChannelBalance struct {
	// Available is what the application can spend on the channel right now:
	// Own plus Pool. A message priced above it is refused with 402. Use
	// [ChannelBalance.AvailableCents] to compare it with a floor.
	Available string `json:"available"`
	// Own is the application's own funds on the channel.
	Own string `json:"own"`
	// Pool is the available amount of the shared pool backing the channel;
	// nil when the channel stands alone.
	Pool *string `json:"pool"`
	// Low is true when Available is at or below the company's threshold.
	Low bool `json:"low"`
}

// BalancePool is a shared top-up pool: funds of the company that several
// channels and applications spend after their own.
type BalancePool struct {
	// Name is the pool's name in the console; empty when it has none.
	Name string `json:"name"`
	// Channel is "sms", "viber" or "noti" when the pool is limited to one
	// channel, empty when it backs every channel.
	Channel string `json:"channel"`
	// Balance is the amount the pool can cover right now.
	Balance string `json:"balance"`
}

// wireBalancePool reads the pool's two nullable text fields.
type wireBalancePool struct {
	Name    *string `json:"name"`
	Channel *string `json:"channel"`
	Balance string  `json:"balance"`
}

type wireBalance struct {
	Balance
	Pools []wireBalancePool `json:"pools"`
}

// Balance reads the application's balance, per channel. It is a snapshot,
// not a reservation: use it to warn before a send fails with
// [ErrInsufficientFunds], not to guarantee one. The amounts already reflect
// messages that are booked and not yet charged.
//
// On the sandbox host the sandbox token returns the application's real
// balance; nothing is mocked, and sandbox sends never change it.
func (c *Client) Balance(ctx context.Context) (Balance, error) {
	var w wireBalance
	a, err := c.call(ctx, request{method: http.MethodGet, path: "balance"}, &w)
	if err != nil {
		return Balance{}, err
	}
	if w.Currency == "" {
		return Balance{}, &unexpectedError{reason: "the answer has no currency"}
	}
	b := w.Balance
	if b.Channels == nil {
		b.Channels = map[string]ChannelBalance{}
	}
	for _, p := range w.Pools {
		bp := BalancePool{Balance: p.Balance}
		if p.Name != nil {
			bp.Name = *p.Name
		}
		if p.Channel != nil {
			bp.Channel = *p.Channel
		}
		b.Pools = append(b.Pools, bp)
	}
	b.RateLimitRemaining = -1
	if n, err := strconv.Atoi(a.header.Get("X-RateLimit-Remaining")); err == nil && n >= 0 {
		b.RateLimitRemaining = n
	}
	b.Raw = a.raw
	return b, nil
}

// errAmount is what a malformed amount returns; it never quotes the amount.
var errAmount = errors.New("connectix: the amount is not a decimal number")

// AvailableCents returns Available as whole cents of the currency, rounding
// toward zero below the cent ("0.0049" is 0, "-0.0150" is -1), so a caller
// can compare it with a floor in cents. It refuses anything that is not an
// optionally signed decimal with digits on both sides of an optional point
// (an exponent, a comma, a space, an empty string) or that does not fit an
// int64, and its error never quotes the amount.
func (b ChannelBalance) AvailableCents() (int64, error) {
	s := b.Available
	neg := false
	switch {
	case strings.HasPrefix(s, "-"):
		neg, s = true, s[1:]
	case strings.HasPrefix(s, "+"):
		s = s[1:]
	}
	whole, frac, hasPoint := strings.Cut(s, ".")
	if !digits(whole) || (hasPoint && !digits(frac)) {
		return 0, errAmount
	}
	units, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || units > (math.MaxInt64-99)/100 {
		return 0, errAmount
	}
	cents := units * 100
	frac = (frac + "00")[:2]
	f, _ := strconv.ParseInt(frac, 10, 64) // two digits, checked above
	cents += f
	if neg {
		cents = -cents
	}
	return cents, nil
}

// digits reports whether s is one or more ASCII digits.
func digits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

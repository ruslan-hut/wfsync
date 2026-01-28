package stripeclient

import (
	"errors"
	"fmt"

	"github.com/stripe/stripe-go/v76"
)

func (s *StripeClient) parseErr(err error) error {
	var stripeErr *stripe.Error
	if errors.As(err, &stripeErr) {
		return fmt.Errorf("stripe error (status %d, code %s): %s",
			stripeErr.HTTPStatusCode,
			stripeErr.Code,
			stripeErr.Msg)
	}
	return err
}

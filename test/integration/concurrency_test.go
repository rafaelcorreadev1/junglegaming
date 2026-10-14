//go:build integration

package integration

import (
	"testing"

	"github.com/rafaelcorreadev1/junglegaming/test/e2e"
)

func TestSameBetSentFiftyTimesInParallelDebitsOnce(t *testing.T) {
	e2e.SameBetInParallel(t, newClient(t))
}

func TestTwoBetsCompetingForTheSameBalance(t *testing.T) {
	e2e.CompetingBets(t, newClient(t))
}

func TestDistinctWalletsAdvanceInParallel(t *testing.T) {
	e2e.DistinctWallets(t, newClient(t))
}

func TestSameOperationOverHTTPAndSQSHasOneEffect(t *testing.T) {
	e2e.SameOperationOverHTTPAndSQS(t, newClient(t), e2e.NewQueue(t))
}

func TestRepeatedSQSDeliveryIsAppliedOnce(t *testing.T) {
	e2e.RepeatedDeliveryIsAppliedOnce(t, newClient(t), e2e.NewQueue(t))
}

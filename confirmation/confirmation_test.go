package confirmation

import (
	"errors"
	"testing"
	"time"
)

func TestConfirmationValidationRequiresCorrelatedYesNo(t *testing.T) {
	request := Request{ID: "confirm_1", SessionID: "session", TurnID: "turn", CallID: "call", ToolName: "publish", Message: "Publish?", CreatedAt: time.Now()}
	if err := ValidateRequest(request); err != nil {
		t.Fatal(err)
	}
	decision := Decision{ConfirmationID: request.ID, SessionID: request.SessionID, TurnID: request.TurnID, CallID: request.CallID, Answer: Yes}
	if err := ValidateDecision(decision); err != nil {
		t.Fatal(err)
	}
	decision.Answer = "maybe"
	if err := ValidateDecision(decision); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid answer error = %v", err)
	}
}

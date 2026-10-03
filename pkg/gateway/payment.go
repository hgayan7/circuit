package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// PaymentAccount defines a governed payment and billing account.
type PaymentAccount struct {
	ID                        string   `yaml:"id" json:"id"`
	Name                      string   `yaml:"name" json:"name"`
	Currency                  string   `yaml:"currency,omitempty" json:"currency,omitempty"`
	AllowedDestinations       []string `yaml:"allowed_destinations,omitempty" json:"allowed_destinations,omitempty"`
	MaxTransactionAmount      float64  `yaml:"max_transaction_amount,omitempty" json:"max_transaction_amount,omitempty"`
	AutoApprovalThreshold     float64  `yaml:"auto_approval_threshold,omitempty" json:"auto_approval_threshold,omitempty"`
	RequireApprovalForRefunds bool     `yaml:"require_approval_for_refunds,omitempty" json:"require_approval_for_refunds,omitempty"`
	InitialBalance            float64  `yaml:"initial_balance,omitempty" json:"initial_balance,omitempty"`
	Timeout                   time.Duration

	simMu        sync.RWMutex
	balance      float64
	transactions []map[string]any
	charges      map[string]map[string]any
	refunds      map[string]map[string]any
	txSeq        int
	chargeSeq    int
	refundSeq    int
}

// NewPaymentAccount creates a PaymentAccount with simulated backend ledger.
func NewPaymentAccount(id, name, currency string, allowedDestinations []string, maxTxAmount, autoApprovalThreshold float64, reqApprovalRefunds bool, initialBalance float64, timeout time.Duration) (*PaymentAccount, error) {
	if id == "" {
		return nil, fmt.Errorf("payment account ID is required")
	}
	if currency == "" {
		currency = "USD"
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	return &PaymentAccount{
		ID:                        id,
		Name:                      name,
		Currency:                  strings.ToUpper(currency),
		AllowedDestinations:       allowedDestinations,
		MaxTransactionAmount:      maxTxAmount,
		AutoApprovalThreshold:     autoApprovalThreshold,
		RequireApprovalForRefunds: reqApprovalRefunds,
		InitialBalance:            initialBalance,
		Timeout:                   timeout,
		balance:                   initialBalance,
		transactions:              make([]map[string]any, 0),
		charges:                   make(map[string]map[string]any),
		refunds:                   make(map[string]map[string]any),
	}, nil
}

// PaymentExecutor executes governed financial actions on a PaymentAccount.
type PaymentExecutor struct {
	account *PaymentAccount
}

// NewPaymentExecutor creates a PaymentExecutor.
func NewPaymentExecutor(account *PaymentAccount) *PaymentExecutor {
	return &PaymentExecutor{account: account}
}

func (e *PaymentExecutor) Execute(ctx context.Context, r Request) Outcome {
	return simulationOutcome(e.execute(ctx, r))
}

func (e *PaymentExecutor) execute(ctx context.Context, r Request) Outcome {
	if e.account.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.account.Timeout)
		defer cancel()
	}

	switch r.Operation {
	case "transfer_funds":
		return e.transferFunds(ctx, r)
	case "create_charge":
		return e.createCharge(ctx, r)
	case "issue_refund":
		return e.issueRefund(ctx, r)
	case "get_balance":
		return e.getBalance(ctx, r)
	default:
		return Outcome{Status: 400, Error: fmt.Sprintf("unsupported payment operation %q", r.Operation)}
	}
}

func (e *PaymentExecutor) transferFunds(ctx context.Context, r Request) Outcome {
	amount := floatVal(r.Args, "amount")
	dest := text(r.Args, "destination")
	currency := strings.ToUpper(text(r.Args, "currency"))
	if currency == "" {
		currency = e.account.Currency
	}
	reason := text(r.Args, "reason")

	if amount <= 0 {
		return Outcome{Status: 400, Error: "amount must be greater than 0"}
	}
	if currency != e.account.Currency {
		return Outcome{Status: 400, Error: fmt.Sprintf("currency %q does not match account currency %q", currency, e.account.Currency)}
	}
	if e.account.MaxTransactionAmount > 0 && amount > e.account.MaxTransactionAmount {
		return Outcome{Status: 400, Error: fmt.Sprintf("transfer amount %.2f exceeds maximum permitted transaction amount of %.2f", amount, e.account.MaxTransactionAmount)}
	}
	if len(e.account.AllowedDestinations) > 0 && !member(e.account.AllowedDestinations, dest) {
		return Outcome{Status: 400, Error: fmt.Sprintf("destination %q is not in the allowed destinations list", dest)}
	}

	e.account.simMu.Lock()
	defer e.account.simMu.Unlock()

	if amount > e.account.balance {
		return Outcome{Status: 400, Error: fmt.Sprintf("insufficient funds: available balance is %.2f %s, requested %.2f %s", e.account.balance, e.account.Currency, amount, currency)}
	}

	e.account.balance -= amount
	e.account.txSeq++
	txID := fmt.Sprintf("tx_%05d", e.account.txSeq)

	record := map[string]any{
		"tx_id":         txID,
		"account_id":    e.account.ID,
		"destination":   dest,
		"amount":        amount,
		"currency":      currency,
		"reason":        reason,
		"balance_after": e.account.balance,
		"status":        "succeeded",
		"timestamp":     time.Now().UTC().Format(time.RFC3339),
	}
	e.account.transactions = append(e.account.transactions, record)

	body, _ := json.Marshal(record)
	return Outcome{Status: 200, Body: body}
}

func (e *PaymentExecutor) createCharge(ctx context.Context, r Request) Outcome {
	amount := floatVal(r.Args, "amount")
	customerID := text(r.Args, "customer_id")
	currency := strings.ToUpper(text(r.Args, "currency"))
	if currency == "" {
		currency = e.account.Currency
	}
	description := text(r.Args, "description")

	if amount <= 0 {
		return Outcome{Status: 400, Error: "amount must be greater than 0"}
	}
	if currency != e.account.Currency {
		return Outcome{Status: 400, Error: fmt.Sprintf("currency %q does not match account currency %q", currency, e.account.Currency)}
	}
	if e.account.MaxTransactionAmount > 0 && amount > e.account.MaxTransactionAmount {
		return Outcome{Status: 400, Error: fmt.Sprintf("charge amount %.2f exceeds maximum permitted transaction amount of %.2f", amount, e.account.MaxTransactionAmount)}
	}

	e.account.simMu.Lock()
	defer e.account.simMu.Unlock()

	e.account.balance += amount
	e.account.chargeSeq++
	chargeID := fmt.Sprintf("ch_%05d", e.account.chargeSeq)

	record := map[string]any{
		"charge_id":       chargeID,
		"account_id":      e.account.ID,
		"customer_id":     customerID,
		"amount":          amount,
		"currency":        currency,
		"description":     description,
		"amount_refunded": 0.0,
		"status":          "succeeded",
		"timestamp":       time.Now().UTC().Format(time.RFC3339),
	}
	e.account.charges[chargeID] = record

	body, _ := json.Marshal(record)
	return Outcome{Status: 200, Body: body}
}

func (e *PaymentExecutor) issueRefund(ctx context.Context, r Request) Outcome {
	chargeID := text(r.Args, "charge_id")
	amount := floatVal(r.Args, "amount")
	reason := text(r.Args, "reason")

	e.account.simMu.Lock()
	defer e.account.simMu.Unlock()

	charge, ok := e.account.charges[chargeID]
	if !ok {
		return Outcome{Status: 404, Error: fmt.Sprintf("charge %q not found", chargeID)}
	}

	chargeAmount, _ := charge["amount"].(float64)
	refundedAmount, _ := charge["amount_refunded"].(float64)
	availableRefund := chargeAmount - refundedAmount

	if amount <= 0 {
		amount = availableRefund
	}
	if amount <= 0 || amount > availableRefund {
		return Outcome{Status: 400, Error: fmt.Sprintf("invalid refund amount %.2f: maximum refundable amount is %.2f", amount, availableRefund)}
	}
	if amount > e.account.balance {
		return Outcome{Status: 400, Error: fmt.Sprintf("insufficient account funds: available balance is %.2f %s, refund requires %.2f", e.account.balance, e.account.Currency, amount)}
	}

	e.account.balance -= amount
	newRefunded := refundedAmount + amount
	charge["amount_refunded"] = newRefunded
	if newRefunded >= chargeAmount {
		charge["status"] = "refunded"
	} else {
		charge["status"] = "partially_refunded"
	}

	e.account.refundSeq++
	refundID := fmt.Sprintf("rf_%05d", e.account.refundSeq)

	record := map[string]any{
		"refund_id":     refundID,
		"charge_id":     chargeID,
		"account_id":    e.account.ID,
		"amount":        amount,
		"currency":      e.account.Currency,
		"reason":        reason,
		"balance_after": e.account.balance,
		"status":        "succeeded",
		"timestamp":     time.Now().UTC().Format(time.RFC3339),
	}
	e.account.refunds[refundID] = record

	body, _ := json.Marshal(record)
	return Outcome{Status: 200, Body: body}
}

func (e *PaymentExecutor) getBalance(ctx context.Context, r Request) Outcome {
	e.account.simMu.RLock()
	defer e.account.simMu.RUnlock()

	res := map[string]any{
		"account_id":         e.account.ID,
		"name":               e.account.Name,
		"balance":            e.account.balance,
		"currency":           e.account.Currency,
		"transactions_count": len(e.account.transactions),
		"charges_count":      len(e.account.charges),
		"refunds_count":      len(e.account.refunds),
	}
	body, _ := json.Marshal(res)
	return Outcome{Status: 200, Body: body}
}

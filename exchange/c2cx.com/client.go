package c2cx

import (
	"time"

	"strings"

	"sync"

	"github.com/pkg/errors"
	"github.com/uberfurrer/tradebot/exchange"
	"github.com/uberfurrer/tradebot/logger"
)

// Client implements exchange.Client interface
// Client track all orders that was created using it
type Client struct {
	// Key and Secret needs for creating and accessing orders, update them
	// You may use Client without it for tracking OrderBook
	Key, Secret     string
	RefreshInterval time.Duration

	// Tracker provides provides functionality for tracking orders
	// if Tracker == nil then orders does not tracked and Client will be update only OrderBook directly
	// If Key and Secret are right, all functions will work correctly
	Tracker *exchange.OrderTracker

	// OrderBookTracker provides functionality for tracking OrderBook
	// It use RefreshRate in milliseconds for updating
	// OrderBookTracker should be free for concurrent use
	OrderBookTracker exchange.OrderBookTracker

	// Stop stops updating
	// After sending to this, you need to restart Client.Update()
	Stop chan struct{}

	prevUpdate time.Time
	sem        chan struct{}
}

// Cancel cancels order with given orderID
func (c *Client) Cancel(orderID int) (*exchange.OrderInfo, error) {
	err := CancelOrder(c.Key, c.Secret, orderID)
	if err != nil {
		return nil, err
	}
	c.Tracker.Cancel(orderID)
	order, err := c.Tracker.Get(orderID)
	return &order, err
}

// CancelAll cancels all executed orders, that was created using this cilent
func (c *Client) CancelAll() ([]*exchange.OrderInfo, error) {
	orders := c.Tracker.Executed()
	var result = make([]*exchange.OrderInfo, 0, len(orders))
	for _, v := range orders {
		info, err := c.Cancel(v.OrderID)
		if err != nil {
			return result, err
		}
		result = append(result, info)
	}
	return result, nil
}

// CancelMarket cancels all order with given symbol that was created using this client
func (c *Client) CancelMarket(symbol string) ([]*exchange.OrderInfo, error) {
	symbol, err := normalize(symbol)
	if err != nil {
		return nil, err
	}
	orders := c.Tracker.Executed()
	var result = make([]*exchange.OrderInfo, 0, len(orders))
	for _, v := range orders {
		if v.TradePair == symbol {
			info, err := c.Cancel(v.OrderID)
			if err != nil {
				return result, err
			}
			result = append(result, info)
		}
	}
	return result, nil
}

// Buy place buy order
func (c *Client) Buy(symbol string, price, amount float64) (orderID int, err error) {
	symbol, err = normalize(symbol)
	if err != nil {
		return
	}

	orderID, err = CreateOrder(c.Key, c.Secret, symbol, "buy", PriceTypeMarket, amount, 1, price, nil, nil, nil)
	if err != nil {
		return
	}
	c.Tracker.NewOrder(symbol, exchange.ActionBuy, exchange.StatusSubmitted, orderID, amount, price)
	return
}

// Sell place sell order
func (c *Client) Sell(symbol string, price, amount float64) (orderID int, err error) {
	symbol, err = normalize(symbol)
	if err != nil {
		return
	}

	orderID, err = CreateOrder(c.Key, c.Secret, symbol, "sell", PriceTypeMarket, amount, 1, price, nil, nil, nil)
	if err != nil {
		return
	}
	c.Tracker.NewOrder(symbol, exchange.ActionSell, exchange.StatusSubmitted, orderID, amount, price)
	return
}

// OrderStatus returns string status of order with given orderID
// Handles only orders that was created using this client
func (c *Client) OrderStatus(orderID int) (string, error) {
	return c.Tracker.Status(orderID)
}

// OrderDetails returns all avalible info about order
// Handles only orders that was created using this client
func (c *Client) OrderDetails(orderID int) (exchange.OrderInfo, error) {
	order, err := c.Tracker.Get(orderID)
	return order, err
}

// Executed wraps Tracker.Executed()
func (c *Client) Executed() []*exchange.OrderInfo { return c.Tracker.Executed() }

// Completed wraps Tracker.Completed()
func (c *Client) Completed() []*exchange.OrderInfo { return c.Tracker.Completed() }

// OrderBook returns interface for managing Orderbook
func (c *Client) OrderBook() exchange.OrderBookTracker {
	return c.OrderBookTracker
}

// GetBalance gets balance information about given currency
func (c *Client) GetBalance(currency string) (string, error) {
	info, err := GetBalance(c.Key, c.Secret)
	if err != nil {
		return "", err
	}
	if v, ok := info[strings.ToLower(currency)]; ok {
		return v, nil
	}
	return "", errors.Errorf("currency %s does not found", currency)
}

func (c *Client) checkUpdate() {
	if c.OrderBookTracker != nil {
		// runs goroutine for each market and wait them
		go func() {
			c.sem <- struct{}{}
			var wg sync.WaitGroup
			wg.Add(len(allowed))
			for _, v := range allowed {
				go func(sym string, w *sync.WaitGroup) {
					defer w.Done()
					orders, err := GetOrderBook(sym)
					if err != nil {
						//log.Printf("c2cx: update orderbook error: %s, %s", err.Error(), sym)
						return
					}
					c.OrderBookTracker.UpdateSym(sym, orders.Bids, orders.Asks)
					return
				}(v, &wg)
			}
			wg.Wait()
			<-c.sem
		}()

	}
	if c.Tracker != nil {
		var wg sync.WaitGroup
		wg.Add(len(allowed) * len(Statusees))
		for _, sym := range allowed {
			for s := range Statusees {
				go func(symbol, status string, w *sync.WaitGroup) {
					defer w.Done()
					orders, err := GetOrderByStatus(c.Key, c.Secret, symbol, status, -1)
					if err != nil {
						logger.Warningf("c2cx: update order info failed %s", err)
						return
					}
					for _, order := range orders {
						var accepted = unixToTime(order.CreateDate)
						switch status {
						case exchange.StatusOpened, exchange.StatusPartial:
							c.Tracker.UpdateOrderDetails(order.OrderID, symbol, &accepted)
						case exchange.StatusCancelled:
							c.Tracker.Cancel(order.OrderID)
						case exchange.StatusCompleted:
							c.Tracker.Complete(order.OrderID, time.Now())
						}
					}
				}(sym, s, &wg)
			}
		}
		wg.Wait()
	}
}

// Update run updates synchronously
func (c *Client) Update() {
	c.sem = make(chan struct{}, 1)
	t := time.NewTicker(c.RefreshInterval * time.Millisecond)
	for {
		select {
		case <-t.C:
			c.checkUpdate()
		case <-c.Stop:
			t.Stop()
			return
		}
	}
}

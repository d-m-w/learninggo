/*****************************************************************************

'tickets' sells movie tickets and manages promotional gifts ("goodies").

This library can be used with the tickets/sample_server program to run as a
service.  It can also be imported directly into some other project to use
without having to go over a network.

*****************************************************************************/
package tickets

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// The itemized receipt for goods actually sold
type Receipt struct {
	Time      interface{}
	Window    int
	ItemsSold []RItem
	Total     int // total amount for all items, in pennies
} // Receipt

// One line of the ItemsSold slice in a Receipt
type RItem struct {
	Desc    string
	Pennies int // amount, in pennies
} // RItem

// A ticket record.
type Ticket struct {
	TicketNum int
	Movie     int
	Showing   int
	Price     int
	SoldOut   bool
	Goodies   bool
	Exchanged bool
	XchOld    string
	XchNew    string
	Window    int
} // Ticket

const (
	TRMovie   = 0 // where's the Movie# in a ticket request tuple?
	TRShowing = 1 // where's the Showing# in a ticket request tuple?
)

// L is the logger to use.
var L log.Logger

// totExchanges is the number of exchanges which have been done.
var totExchanges int

// maxExchanges is the amount of exchangable goods on hand.
// Must not be negative.
var maxExchanges int

// maxMovies is the number of movies the theatre handles simultaneously.
// Requested movie must be  0 <= requested movie < maxMovies
var maxMovies int

// maxShowings is the number of times per day that each movie is show.
// Requested showing must be  0 <= requested showing < maxShowings
var maxShowings int

// maxSeats is the number of seats in each movie room.
// If the incremented number of sold seats is greater than this, then the sale
// is denied due to being sold out.
var maxSeats int

// maxWindows is the number of ticket windows the theatre has.
// Request must come from 1 <= window number <= maxWindows
var maxWindows int

// ticketRoll is a virtual roll of tickets (actually, it is just the ticket
// numbers).  Pulling one off reserves the corresponding ticketRqstDB entry
// in a thread-safe manner.  Channels are the only queue primitive in Go.
var ticketRoll chan int

// ticketRqstDB implements the ticket database internally, since I don't yet
// know how to use a real database with Go.
// Note that this DB tracks both sold tickets and ticket requests which
// couldn't be fulfilled because the requested showing was sold out.
// This allows management to request a lost-opportunity report (not currently
// implemented).
var ticketRqstDB []Ticket

// ticketDBmutex enables the ticket DB to be locked during certain updates.
var ticketDBmutex sync.Mutex

// seatsSold is used to implement a cache of sold-out counters to reduce DB
// queries to determine the count of seats sold for a showing (which would
// otherwise be issued for every ticket request).  There is one counter per
// showing, per movie.
//
// WARNING!  These counters MUST ONLY be accessed with functions of the
//           sync/atomic package, once ticket sales have openned.
var seatsSold [][]int32 // sync/atomic doesn't support plain ints

// The salesOpen flag indicates ticket sales have openned.
// Once this flag is set, all multithreaded access to the tickets system may
// occur at any time.
// There doesn't seem to be a way to update a bool with guaranteed visibilty
// ordering of the update, and no way to query the state of a sync.Once gate,
// which would be a usable substitute for the bool.
var salesOpen bool // WARNING!  This MAY be exposed to visibility problems

// initGate ensures ticket system initialization isn't done multiple times.
var initGate sync.Once

/*  Public error constants  */

// ErrXchNotEntitled  is returned when a goodie exchange is denied because the
// ticket does not entitle the customer to any goodies, either because the movie
// was sold out, or because it was not purchased at a window which offered free
// goodies.
var ErrXchNotEntitled = errors.New("Exchange denied:  customer is not entitled to goodies by this ticket")

// ErrXchAlreadyDone is returned when someone tries to make more than one goodie
// exchange using the same ticket number.
var ErrXchAlreadyDone = errors.New("Exchange denied:  a goodie exchange was already made with this ticket")

// ErrXchOutOfGoods is returned if the goodie exchange is otherwise valid, but
// the theatre has run out of goods to exchange things for.
var ErrXchOutOfGoods = errors.New("Exchange denied:  the theatre has run out of exchange goods")

/*----------------------------------------------------------------------------
tickets.Init(L, MaxMovies, MaxShowings, MaxSeats, MaxWindows)

Public function to initialize the ticket sales system.
Uses private function initOnce() to do actual initialization, if and only if
it has not previously been run.  If called while initOnce() is still running,
then this called to Init() will wait for the in-progress initOnce() to finish.

Parameters:

L
    The Logger to use.  Must not be nil.
MaxExchanges
    The number of goodie exchanges allowed (stock on hand).
    Must be 0 or greater.
MaxMovies
    The number of movies the theatre handles simultaneously.
    Must be at least 1.
MaxShowings
    The number of times per day that each movie is show.
    Must be at least 1.
MaxSeats
    The number of seats in each movie room.
    Must be at least 1.
MaxWindows
    The number of ticket windows the theatre has.
    Must be at least 1.

Returns an error if a parameter is invalid, or nil.
----------------------------------------------------------------------------*/
func Init(parmL *log.Logger, parmMaxExchanges int, parmMaxMovies int, parmMaxShowings int, parmMaxSeats int, parmMaxWindows int) error {
	// Not sure if this is really the right way to do this, but it doesn't
	// APPEAR that sync.Once.Do() returns anything ...
	var initErr error
	initGate.Do(func() {
		initErr = initOnce(parmL, parmMaxExchanges, parmMaxMovies, parmMaxShowings, parmMaxSeats, parmMaxWindows)
	})
	return initErr
} // Init

// This internal routine does the real work of Init.  It is protected by a
// sync.Once gate.  See doc. for Init() for parameters and behaviour.
func initOnce(parmL *log.Logger, parmMaxExchanges int, parmMaxMovies int, parmMaxShowings int, parmMaxSeats int, parmMaxWindows int) error {
	if parmL == nil {
		return errors.New("Missing Logger")
	}
	L = *parmL
	maxExchanges = parmMaxExchanges
	if maxExchanges < 0 {
		return errors.New("MaxExchanges " + strconv.Itoa(maxExchanges) + " must not be negative")
	}
	maxMovies = parmMaxMovies
	if maxMovies < 1 {
		return errors.New("MaxMovies " + strconv.Itoa(maxMovies) + " must be greater than zero")
	}
	maxShowings = parmMaxShowings
	if maxShowings < 1 {
		return errors.New("MaxShowings " + strconv.Itoa(maxMovies) + " must be greater than zero")
	}
	maxSeats = parmMaxSeats
	if maxSeats < 1 {
		return errors.New("MaxSeats " + strconv.Itoa(maxMovies) + " must be greater than zero")
	}
	maxWindows = parmMaxWindows
	if maxWindows < 1 {
		return errors.New("MaxWindows " + strconv.Itoa(maxMovies) + " must be greater than zero")
	}

	seatsSold = make([][]int32, maxMovies, maxMovies)
	for i, _ := range seatsSold {
		seatsSold[i] = make([]int32, maxShowings, maxShowings)
	}

	ticketRqstDB = make([]Ticket, maxMovies*maxShowings*maxSeats+1) // ticketRqstDB[0] is not used

	ticketRoll = make(chan int, 5) // small buffer to minimize read response time
	go ticketProducer(ticketRoll)

	salesOpen = true
	L.Printf("Ticketing system open for sales and exchanges at %s.", time.Now().Format("2006-01-02t15-04-05z-0700"))
	return nil
} // initOnce

//  TODO :  Orderly shutdown.

//  TODO :  Panic shutdown.

// ticketProducer generates sequential ticket numbers and enqueues them ready
// for use on the ticketRoll.  It runs asynchronously, as needed.  Ticket
// numbers start with 1 (this is needed so that having marked the ticket as
// allocated by storing the ticket number into the ticket is different from
// the ticket number field's zero value).
//
// Parameters:
//
// tRoll
//   The ticketRoll queue (an output channel of int).
func ticketProducer(tRoll chan<- int) {
	defer close(tRoll)
	for i := 1; ; i++ {
		tRoll <- i
	}
} //ticketProducer

// nextTicket pulls the next available ticket number off the ticketRoll.  If
// the ticket number is beyond the end of ticketRqstDB, then it logs a fatal
// error and terminates the program.  Otherwise, it marks that Ticket allocated
// in the ticketRqstDB (by setting the TicketNum field in the Ticket), and
// returns the Ticket to the caller.  If no ticket number is available, then it
// waits for one.  If an error occurs on the ticketRoll, it panics, because
// ticket number acquisition is a critical step for all ticket sales.
//
// Because the ticketRoll access is threadsafe, and the ticket number pulled
// off of it is guaranteed unique, it is not necessary to lock the ticketRqstDB
// while marking the Ticket as allocated (Note:  allocation marking is really
// only useful for debugging, or if restart functionality is ever added to the
// ticketing system).
//
// T.B.D.  verify that this is returning a copy of the Ticket not a pointer to
// the ticketRqstDB entry, and fix it if it is returning a pointer to the DB entry.
//
// After updating a copy of the Ticket, the caller will need to call
// UpdateTicket to commit the Ticket changes into the DB.
func nextTicket() (Ticket, error) {
	t, stillOpen := <-ticketRoll
	if !stillOpen {
		return *new(Ticket), errors.New("Cannot get another ticket:  ticketRoll has been closed and drained.")
	}

	if t >= len(ticketRqstDB) {
		L.Printf("nextTicket cannot continue:  new number %d exceeds capacity of ticketRqstDB (last element is [%d]).  Aborting ...", t, (len(ticketRqstDB) - 1))
		b := make([]byte, 16*1024)
		u := runtime.Stack(b, true)
		L.Printf("\n%s\n", bytes.NewBuffer(b[:u]).String())
		os.Exit(1) // panic doesn't work in a web server - it just kills the current transaction
	}

	// Mark the Ticket as in-use, in case of restart/recovery (not implemented in the initial release).
	// Nobody else knows this ticket number, yet, so we don't need a lock.
	ticketRqstDB[t].TicketNum = t

	return ticketRqstDB[t], nil
} // nextTicket

// readTicket reads the specified ticket from the ticketRqstDB and returns a
// copy.
//
// T.B.D.  verify that this is returning a copy of the Ticket not a pointer to
// the ticketRqstDB entry, and fix it if it is returning a pointer to the DB entry.
//
// After updating a copy of the Ticket, the caller will need to call
// UpdateTicket to commit the Ticket changes into the DB.
//
// The DB is locked during the read.  Record locking would be better, though.
// In the initial implementation, locking is not strictly necessary, as long as:
//   A. Reporting is not run while the ticket windows are open
//   B. No tickets are made available for further processing (i.e. exchanges)
//      until the DB update from initially selling the ticket is known to have
//      been committed (the calling application just has to wait for Sell() to
//      return, before exposing the ticket).
//   C. No additional sorts of ticket DB updates (especially asynchronous ones)
//      are implemented.
// To avoid accidents if the application is changed, locking is already implemented.
func readTicket(tickNum int) (Ticket, error) {
	var t Ticket

	if tickNum < 1 || tickNum >= len(ticketRqstDB) /* Don't need the lock for this, bec. ticketRqstDB cannot shrink. */ {
		return t, fmt.Errorf("readTicket failed:  tickNum %d outside the DB", tickNum)
	}

	ticketDBmutex.Lock()
	defer ticketDBmutex.Unlock()
	switch tickNum {
	case 0:
		return t, fmt.Errorf("readTicket failed:  Ticket %d is not allocated.", tickNum)
	case ticketRqstDB[tickNum].TicketNum:
		// Good  --  it's an active Ticket
		t.TicketNum = ticketRqstDB[tickNum].TicketNum
		t.Movie = ticketRqstDB[tickNum].Movie
		t.Showing = ticketRqstDB[tickNum].Showing
		t.Price = ticketRqstDB[tickNum].Price
		t.SoldOut = ticketRqstDB[tickNum].SoldOut
		t.Goodies = ticketRqstDB[tickNum].Goodies
		t.Exchanged = ticketRqstDB[tickNum].Exchanged
		t.XchOld = ticketRqstDB[tickNum].XchOld
		t.XchNew = ticketRqstDB[tickNum].XchNew
		t.Window = ticketRqstDB[tickNum].Window
	default:
		panic(fmt.Sprintf("readTicket failed:  tickNum %d requested, but Ticket marked with TicketNum %d  --  either the database is corrupted or there is an internal logic error  --  NOTIFY SUPPORT!  System shutting down.", tickNum, ticketRqstDB[tickNum].TicketNum))
	}
	return t, nil
} // readTicket

// checkAvailabilityAndPrice determines whether there are any seats left for
// the specified showing of the specified movie, and if so, consumes one of
// them.  The price of the ticket is also determined.
//
// The current implementation uses the seatsSold cache instead of querying
// the ticketRqstDB.
//
// Parameters:
//
// m
//    The movie number to be checked.
// s
//    The showing to be checked.
//
// Returns:
//
// priceInPennies
//    The price of the ticket as an int, in pennies (to keep FP roundoff out of
//    your wallet!).  If the showing is sold out, then this price may not be
//    valid.
// soldOut
//    True if the showing is already sold out.  The caller must not sell the
//    ticket to the customer, but should update the Ticket in the DB to show
//    that it couldn't be sold because this showing of this movie was already
//    sold out.
//
// Note:  if the processing of this ticket request fails after
// checkAvailabilityAndPrice(), then the seat in that showing may go unsold.
func checkAvailabilityAndPrice(m int, s int) (priceInPennies int, soldOut bool) {
	priceInPennies = 1000 // Initially, all tickets cost $10.00

	consumedSeatsIncludingThisOne := atomic.AddInt32(&seatsSold[m][s], 1)

	return priceInPennies, (int(consumedSeatsIncludingThisOne) > maxSeats)
} // checkAvailabilityAndPrice

// updateTicketExchange uses the supplied Ticket struct to update the product
// exchange fields in the ticket in ticketRqstDB with the same ticket number.
// updateTicketExchange and updateTicketSale are kept as separate functions,
// to ensure that the DB is not corrupted if goroutine dispatching results in
// the exchange being recorded before the sale is entered in the DB (I think
// this should be impossible, but better safe than corrupt).
//
// Note that ONLY the product exchange fields are updated by this function.
//
// See doc. for readTicket(), and TODO comments in Exchange(), for locking
// considerations.
func updateTicketExchange(t Ticket) error {
	if t.TicketNum < 1 || t.TicketNum >= len(ticketRqstDB) /* Don't need the lock for this, bec. ticketRqstDB cannot shrink. */ {
		return fmt.Errorf("updateTicketExchange failed:  TicketNum %d outside the DB", t.TicketNum)
	}

	ticketDBmutex.Lock()
	defer ticketDBmutex.Unlock()
	ticketRqstDB[t.TicketNum].Exchanged = t.Exchanged
	ticketRqstDB[t.TicketNum].XchOld = t.XchOld
	ticketRqstDB[t.TicketNum].XchNew = t.XchNew

	return nil
} // updateTicketExchange

// updateTicketSale uses the supplied Ticket struct to update the sales-related
// fields of the Ticket in the ticketRqstDB with the same ticket number.  The
// product exchange fields are IGNORED (c/f updateTicketExchange).  This
// function needs to be called when the ticket is either sold, or the showing
// determined to be sold out when the sale was attempted.
//
// See doc. for readTicket(), and TODO comments in Exchange(), for locking
// considerations.
func updateTicketSale(t Ticket) error {
	if t.TicketNum < 1 || t.TicketNum >= len(ticketRqstDB) /* Don't need the lock for this, bec. ticketRqstDB cannot shrink. */ {
		return fmt.Errorf("updateTicketSale failed:  TicketNum %d outside the DB", t.TicketNum)
	}

	ticketDBmutex.Lock()
	defer ticketDBmutex.Unlock()
	ticketRqstDB[t.TicketNum].Movie = t.Movie
	ticketRqstDB[t.TicketNum].Showing = t.Showing
	ticketRqstDB[t.TicketNum].Price = t.Price
	ticketRqstDB[t.TicketNum].SoldOut = t.SoldOut
	ticketRqstDB[t.TicketNum].Goodies = t.Goodies
	ticketRqstDB[t.TicketNum].Window = t.Window

	return nil
} // updateTicketSale

// Exchange is used to exchange goodies which the customer has received.
//
// Parameters:
//
// tickNum
//    The ticket number under which the customer received the goodie to be
//    exchanged.
// oldGoodie
//    The item to be exchanged.
// newGoodie
//    The replacement item.
//
// Returns:
//
// err
//    if the exchange was denied, the ticket number was invalid, or some system
//    error occurred while recording the exchange. See the doc. for the ErrXch*
//    variables for possible data-driven reasons for denial of the exchange.
//
//    An error is also returned if the salesOpen (system up) flag is not set.
func Exchange(tickNum int, oldGoodie string, newGoodie string) error {

	if !salesOpen {
		return errors.New("Exchange failed:  ticketing system is down.")
	}

	t, err := readTicket(tickNum)
	if err != nil {
		return fmt.Errorf("Exchange failed:  %v", err)
	}

	if t.SoldOut {
		return ErrXchNotEntitled
	}

	if !t.Goodies {
		return ErrXchNotEntitled
	}

	if t.Exchanged {
		return ErrXchAlreadyDone
	}

	if totExchanges >= maxExchanges {
		return ErrXchOutOfGoods
	}

	totExchanges++
	t.Exchanged = true
	t.XchOld = oldGoodie
	t.XchNew = newGoodie

	//  TODO :  Ensure that no one else can do an exchange update "inside" of
	//  TODO :  this one.  Absent an SQL DB w/ appropriate level of txn
	//  TODO :  isolation, or holding the DB lock throughout the Exchange call,
	//  TODO :  there's no protection against simultaneous Exchanges (although
	//  TODO :  shouldn't be possible, unless either someone forges a duplicate
	//  TODO :  printed ticket, or the customer tries two exchanges AND the
	//  TODO :  ticketing system suffered a long delay in recording the first
	//  TODO :  exchange.  These events can't happen unless someone codes them
	//  TODO :  into the simulation, but should be fixed before anyone tries to
	//  TODO :  make a "real" ticketing system.
	//  TODO :  See doc. for readTicket() for further discussion of DB locking.
	err = updateTicketExchange(t)
	if err != nil {
		return fmt.Errorf("Exchange failed:  %v", err)
	}

	return nil
} // Exchange

// Sell is used when a customer requests to buy one or more tickets.
// This may result in any combination of compleated sales and sales denied
// because the showing is sold out.
//
// Parameters:
//
// window
//    Which ticket window is conducting this sale.
// ticketRequests
//    One or more ticket requests.  Each request consists of a [2]int, which
//    gives the movie and showing numbers.
// paymentInfo
//    Reserved for future use.  The exact composition of this data is not
//    currently defined.
// localTime
//    Copied as-is as the receipt's timestamp.
//    In the initial implementation, this field is opaque, and no other
//    use or validation is made of it.  In the future, its format should
//    be defined and it should be validated against a clock sync limit.
//
// Returns:
//
// tickets
//    An array of Tickets, which can be a mix of valid tickets and sold-out
//    placeholders.  This is in the same order as the incoming ticketRequests.
// receipt
//    A receipt for whatever tickets were actually sold, if any.
// err
//    Any error which occurred.
//      * The window and movie information is validated, but the initial imple-
//        mentation ignores the paymentInfo and localTime fields.
//      * Any internal error which occurs is passed through.
//      * An error is returned if the salesOpen (system up) flag is not set.
func Sell(window int, ticketRequests [][2]int, paymentInfo map[string]interface{}, localTime interface{}) (tickets []Ticket, receipt Receipt, err error) {

	if !salesOpen {
		return tickets, receipt, errors.New("Sell failed:  ticketing system is down.")
	}

	var totalprice = 0 // in pennies
	tickets = make([]Ticket, len(ticketRequests), len(ticketRequests))
	receipt = Receipt{Time: localTime, Window: window}

	if window < 1 || window > maxWindows {
		return tickets, receipt, fmt.Errorf("Sell failed:  window %d out of range.  Must be between 1 and %d, inclusive.", window, maxWindows)
	}
	// Validation and use of localTime not currently implemented.
	// Validation and use of paymentInfo not currently implemented.

	// Edit as much as possible before consuming tickets in the DB
	for i, trqst := range ticketRequests {
		movie := trqst[TRMovie]
		if movie < 0 || movie >= maxMovies {
			return tickets, receipt, fmt.Errorf("Sell failed:  ticket request %d:  movie# %d not between 0 and %d", (i + 1), movie, maxMovies)
		}
		showing := trqst[TRShowing]
		if showing < 0 || showing >= maxShowings {
			return tickets, receipt, fmt.Errorf("Sell failed:  ticket request %d:  showing %d not between 0 and %d", (i + 1), showing, maxShowings)
		}
	}

	for i, trqst := range ticketRequests {
		t, err := nextTicket()
		if err != nil {
			return tickets, receipt, fmt.Errorf("Sell failed:  ticket request %d:  %v", (i + 1), err)
		}
		t.Movie = trqst[TRMovie]
		t.Showing = trqst[TRShowing]
		t.Window = window
		t.Price, t.SoldOut = checkAvailabilityAndPrice(t.Movie, t.Showing)
		if !t.SoldOut {
			totalprice += t.Price
			if window == 1 {
				t.Goodies = true
			}
			item := RItem{Desc: fmt.Sprintf("Movie %d, Showing %d", t.Movie, t.Showing), Pennies: t.Price}
			receipt.ItemsSold = append(receipt.ItemsSold, item)
		}
		tickets[i] = t
		err = updateTicketSale(t)
		if err != nil {
			return tickets, receipt, fmt.Errorf("Sell failed:  ticket request %d:  %v", (i + 1), err)
		}

	}

	receipt.Total = totalprice

	L.Printf("Sell for window %d returning:\n\ttickets:\n%+v\n\treceipt:\n%+v\n", window, tickets, receipt)

	return tickets, receipt, nil

} // Sell

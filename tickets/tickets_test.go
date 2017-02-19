package tickets

import (
	"log"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

var lastTickNum = 0                           // ticket # 0 is never used
var ticketForExchange = Ticket{TicketNum: -1} // flag as not a real ticket now
var ticketNotExchange = Ticket{TicketNum: -1} // flag as not a real ticket now

func TestInitAndTicketProducer(tst *testing.T) {
	Ltest := log.New(os.Stderr, "TestInit:  ", log.Ldate|log.Ltime|log.Llongfile)
	ierr := Init(Ltest, 5, 6, 7, 8, 9)
	if ierr != nil {
		tst.Errorf("Init(Ltest,5,6,7,8,9) returned error %v", ierr)
	}
	if maxExchanges != 5 {
		tst.Errorf("Init(Ltest,5,6,7,8,9) maxExchanges is %d not 5", maxExchanges)
	}
	if maxMovies != 6 {
		tst.Errorf("Init(Ltest,5,6,7,8,9) maxMovies is %d not 6", maxMovies)
	}
	if maxShowings != 7 {
		tst.Errorf("Init(Ltest,5,6,7,8,9) maxShowings is %d not 7", maxShowings)
	}
	if maxSeats != 8 {
		tst.Errorf("Init(Ltest,5,6,7,8,9) maxSeats is %d not 8", maxSeats)
	}
	if maxWindows != 9 {
		tst.Errorf("Init(Ltest,5,6,7,8,9) maxWindows is %d not 9", maxWindows)
	}
	/*********
	     * This test always fails, even if the assignment works.
	     * Running it with the Delve debugger shows that the L log.Logger's
	     * contents have changed from all zero values to now match the values
	     * in the *parmL log.Logger.  But trying to do "print &L == parmL" in
	     * the debugger gets a type mis-match error between "*struct log.Logger"
	     * and "*log.Logger".  Trying to "print &L" and "print &*parmL" (just
	     * "print parmL" is automatically dereferenced) shows wildly different
	     * addressses, and again distinguishes between "*struct log.Logger" and
	     * "*log.Logger".
	  if  &L != Ltest  {
	      tst.Error("Init(Ltest,5,6,7,8,9) L is not *Ltest")
	  }
	     * Therefore, have to remove the test, for now.
	 *********/
	if len(seatsSold) != maxMovies {
		tst.Errorf("Init(Ltest,5,6,7,8,9) seatsSold is %d long, expecting %d", len(seatsSold), maxMovies)
	}
	for i, s := range seatsSold {
		if len(s) != maxShowings {
			tst.Errorf("Init(Ltest,5,6,7,8,9) seatsSold[%d] is %d long, expecting %d", i, len(s), maxShowings)
		}
	}
	trl := maxMovies*maxShowings*maxSeats + 1
	if len(ticketRqstDB) != trl {
		tst.Errorf("Init(Ltest,5,6,7,8,9) ticketRqstDB is %d tickets long, expecting %d", len(ticketRqstDB), trl)
	}
	if ticketRoll == nil {
		tst.Error("Init(Ltest,5,6,7,8,9) did not create ticketRoll")
	} else {
		t, ok := <-ticketRoll
		lastTickNum++
		if t != lastTickNum || !ok { // !ok == EOF on a closed channel
			tst.Errorf("Init(Ltest,5,6,7,8,9) ticketRoll returned %d, %t when %d, true were expected", t, ok, lastTickNum)
		}
		lastTickNum = t
	}
	if !salesOpen {
		tst.Error("Init(Ltest,5,6,7,8,9) did not set salesOpen flag")
	}
} // TestInitAndTicketProducer

// This test should occur before testing selling or exchanging.
// The  _other_  nextTicket test needs to be the last test.
func TestNextTicket(tst *testing.T) {
	t, err := nextTicket()
	lastTickNum++
	if err != nil {
		tst.Errorf("nextTicket() returned error %v", err)
	}
	if t.TicketNum != lastTickNum {
		tst.Errorf("nextTicket() returned a ticket identifying itself as # %d.  Expected %d", t.TicketNum, lastTickNum)
		lastTickNum = t.TicketNum
	}
	if ticketRqstDB[lastTickNum].TicketNum != lastTickNum {
		tst.Errorf("nextTicket() didn't set the TicketNum field correctly in ticketRqstDB[%d] - found %d", lastTickNum, ticketRqstDB[lastTickNum].TicketNum)
	}
} // TestNextTicket

func TestSellAndCheckAvailabilityAndPrice(tst *testing.T) {
	atomic.StoreInt32(&seatsSold[1][2], 0)
	pennies, soldOut := checkAvailabilityAndPrice(1, 2)
	ss12 := atomic.LoadInt32(&seatsSold[1][2])
	if pennies != 1000 || soldOut || ss12 != 1 {
		tst.Errorf("checkAvailabilityAndPrice(1,2) with seatsSold[1][2] = 0, returned price=%d,soldOut=%t,seatsSold[1][2]=%d, expected 1000,false,1", pennies, soldOut, ss12)
	}

	atomic.StoreInt32(&seatsSold[1][2], int32(maxSeats-1))
	pennies, soldOut = checkAvailabilityAndPrice(1, 2)
	ss12 = atomic.LoadInt32(&seatsSold[1][2])
	if pennies != 1000 || soldOut || ss12 != int32(maxSeats) {
		tst.Errorf("checkAvailabilityAndPrice(1,2) with seatsSold[1][2] = maxSeats - 1 = %d, returned price=%d,soldOut=%t,seatsSold[1][2]=%d, expected 1000,false,(maxSeats=%d)", maxSeats-1, pennies, soldOut, ss12, maxSeats)
	}

	atomic.StoreInt32(&seatsSold[1][2], int32(maxSeats))
	pennies, soldOut = checkAvailabilityAndPrice(1, 2)
	ss12 = atomic.LoadInt32(&seatsSold[1][2])
	if !soldOut || ss12 != int32(maxSeats+1) {
		tst.Errorf("checkAvailabilityAndPrice(1,2) with seatsSold[1][2] = 0, returned price=%d,soldOut=%t,seatsSold[1][2]=%d, expected <unreliable_value>,true,(maxSeats+1=%d)", pennies, soldOut, ss12, maxSeats+1)
	}

	sstemp := int32(maxSeats - 3)
	if sstemp < 0 {
		sstemp = 0
	}
	atomic.StoreInt32(&seatsSold[1][2], sstemp) // set precondition so that tickets will be sold
	tickets1, receipt1, err1 := Sell(1, [][2]int{[2]int{1, 2}, [2]int{1, 2}}, make(map[string]interface{}), time.Now())
	tickets2, receipt2, err2 := Sell(maxWindows, [][2]int{[2]int{1, 2}, [2]int{1, 2}}, make(map[string]interface{}), "a dummy time")
	if err1 != nil {
		tst.Errorf("Window 1 Sell call returned error %v", err1)
	}
	if err2 != nil {
		tst.Errorf("Window %d Sell call returned error %v", maxWindows, err2)
	}
	if tickets1[0].TicketNum == tickets1[1].TicketNum ||
		tickets2[0].TicketNum == tickets2[1].TicketNum ||
		tickets1[0].TicketNum == tickets2[0].TicketNum ||
		tickets1[1].TicketNum == tickets2[1].TicketNum ||
		tickets1[0].TicketNum == tickets2[1].TicketNum ||
		tickets1[1].TicketNum == tickets2[0].TicketNum {
		tst.Errorf(`Sell(1,  [][2]int{ [2]int{1,2}, [2]int{1,2}, },  make(map[string]interface{}),  time.Now())
 Sell(maxWindows,  [][2]int{ [2]int{1,2}, [2]int{1,2}, },  make(map[string]interface{}),  "a dummy time")
 should have four unique ticket numbers, but they don't:
 %d, %d, %d, %d`, tickets1[0].TicketNum, tickets1[1].TicketNum, tickets2[0].TicketNum, tickets2[1].TicketNum)
	}
	if tickets1[0].Window != 1 || tickets1[1].Window != 1 || tickets2[0].Window != maxWindows || tickets2[1].Window != maxWindows {
		tst.Errorf("Ticket window #s should be 1, 1, %d, %d, and receipts 1 and %d, but got %d, %d, %d, %d, %d, %d", maxWindows, maxWindows, maxWindows, tickets1[0].Window, tickets1[1].Window, tickets2[0].Window, tickets2[1].Window, receipt1.Window, receipt2.Window)
	}
	if tickets1[0].SoldOut || tickets1[1].SoldOut || tickets2[0].SoldOut || !tickets2[1].SoldOut {
		tst.Errorf("Expected 3 tickets to sell and the last one to be sold out (flags false, false, false, true).\nGot SoldOut flags %t, %t, %t, %t", tickets1[0].SoldOut, tickets1[1].SoldOut, tickets2[0].SoldOut, tickets2[1].SoldOut)
	}
	if !tickets1[0].Goodies || !tickets1[1].Goodies {
		tst.Errorf("Not all tickets from Window 1 have goodies:  %t, %t, expected true, true", tickets1[0].Goodies, tickets1[1].Goodies)
	}
	if tickets2[0].Goodies || tickets2[1].Goodies {
		tst.Errorf("Ticket(s) from Window %d should not have goodies:  %t, %t, expected false, false", maxWindows, tickets2[0].Goodies, tickets2[1].Goodies)
	}
	if len(receipt1.ItemsSold) != 2 || len(receipt2.ItemsSold) != 1 {
		tst.Errorf("First call sold %d, expected 2;  second call sold %d, expected 1.", len(receipt1.ItemsSold), len(receipt2.ItemsSold))
	}
	for i, d := range receipt1.ItemsSold {
		if d.Desc != "Movie 1, Showing 2" || d.Pennies != 1000 {
			tst.Errorf("Line item %d is %s %d, expected %s %d", i, d.Desc, d.Pennies, "Movie 1, Showing 2", 1000)
		}
	}
	if receipt1.Total != len(receipt1.ItemsSold)*1000 || receipt2.Total != len(receipt2.ItemsSold)*1000 {
		tst.Errorf("Receipts total %d and %d, expected 2000 and 1000", receipt1.Total, receipt2.Total)
	}

	//  ATTENTION!  Use the   -v   option to ensure these lines always print.
	ticketForExchange = tickets1[0]
	tst.Logf("TestSellAndCheckAvailabilityAndPrice saving ticket for Exchange success testing:\n%+v\n", ticketForExchange)
	ticketNotExchange = tickets2[0]
	tst.Logf("TestSellAndCheckAvailabilityAndPrice saving ticket for Exchange failure testing:\n%+v\n", ticketNotExchange)
} // TestSellAndCheckAvailabilityAndPrice

func TestExchange(tst *testing.T) {
	if ticketForExchange.TicketNum == -1 || ticketNotExchange.TicketNum == -1 {
		tst.Logf("Cannot peform TestExchange because TestSellAndCheckAvailabilityAndPrice did save any tickets.")
		return
	}

	origExchanges := totExchanges
	t := ticketForExchange.TicketNum
	err := Exchange(t, "water", "soda")
	tst.Logf("\nAfter Exchange(%d, water, soda), ticketRqstDB[%d] record is:\n%+v\n", t, t, ticketRqstDB[t])
	if err != nil {
		tst.Errorf("Attempt to exchange water for soda on ticket # %d failed:  %v", t, err)
	}
	if !ticketRqstDB[t].Exchanged {
		tst.Errorf("Exchanged flag not set on ticketRqstDB[%d]", t)
	}
	if ticketRqstDB[t].XchOld == "" || ticketRqstDB[t].XchNew == "" {
		tst.Errorf("Exchanged goods field(s) not set on ticketRqstDB[%d]:  '%s' exchanged for '%s', expected 'water' exchanged for 'soda'", t, ticketRqstDB[t].XchOld, ticketRqstDB[t].XchNew)
	}
	if totExchanges != origExchanges+1 {
		tst.Errorf("totExchanges not properly incremented from %d to %d.  It is now %d.", origExchanges, origExchanges+1, totExchanges)
	}

	origExchanges = totExchanges
	t = ticketNotExchange.TicketNum
	err = Exchange(t, "water", "soda")
	tst.Logf("\nAfter Exchange(%d, water, soda), ticketRqstDB[%d] record is:\n%+v\n", t, t, ticketRqstDB[t])
	if err != ErrXchNotEntitled {
		tst.Errorf("Attempt to exchange water for soda on ticket # %d should have failed for %v, but got:  %v", t, ErrXchNotEntitled, err)
	}
	if ticketRqstDB[t].Exchanged {
		tst.Errorf("Exchanged flag should not be set on ticketRqstDB[%d]", t)
	}
	if totExchanges != origExchanges {
		tst.Errorf("totExchanges should not have changed when an exchange is not allowed.  Was %d, now %d.", origExchanges, totExchanges)
	}
} // TestExchange

// This test needs to occur last, because it deliberately runs
// the nextTicket() up to its limit.
func TestNextTicketRunsOutOfTickets(tst *testing.T) {
	var t Ticket  // need scope of t to be the whole function
	var err error // can't use := in the 'for' initializer, bec. that would hide the above definition of 't'
	prevtnum := -1

	for t, err = nextTicket(); t.TicketNum < len(ticketRqstDB)-1; t, err = nextTicket() {
		// Loop to consume all of the valid ticket numbers
		if err != nil {
			tst.Errorf("nextTicket() returned error %v when a valid ticket number was expected.  Last valid ticket number returned was %d", err, prevtnum)
			break
		}
		prevtnum = t.TicketNum
	}
	// The above loop should have consumed all of the valid ticket numbers
	if t.TicketNum != len(ticketRqstDB)-1 {
		tst.Errorf("Last ticket number returned from consume-all-valid-ticket-numbers loop was %d, expecting %d", t.TicketNum, len(ticketRqstDB)-1)
	}
	L.Printf("\n\t\t\tThis nextTicket() call should exceed the limit ...")
	t, err = nextTicket()
	if err != ErrNoMoreTickets {
		tst.Errorf("nextTicket() call intended to exceed ticket limit returned error %v, expecting %v (ErrNoMoreTickets)", err, ErrNoMoreTickets)
	}
} // TestNextTicketRunsOutOfTickets

/*****************************************************************************

This program models a movie theatre, accessing the 'tickets' library as a Web
service.

In the initial implementation, customers are not explicitly modeled.

 *****************************************************************************/

package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/d-m-w/learninggo/tickets"
)

type msgHeader struct {
	at   time.Time
	from string
}

type msgDone struct{ head msgHeader }

type msgStop struct {
	head msgHeader
	what string
}

type msgExchange struct {
	head           msgHeader
	tickNum        int
	xchOld, xchNew string
}

type msgTicketSale struct {
	head   msgHeader
	window int
	ticks  []tickets.Ticket
}

type xchData struct {
	head    msgHeader
	tickNum int
}

const (

	// ATTENTION!  constants named Max* are shared with tickets/sample_server and must be kept in sync.
	//             If overridden by a cmd.line option, then the SAME option must be given
	//             to sample_server when it is started.  Unspeakable horrors may result,
	//             elsewise.

	logFileBase                     = "log/theatre."
	name              string        = "theatre model"
	nDelay            time.Duration = time.Second / 10 // can't say 0.1 * time.Second, bec. Duration is an integer
	MaxExchanges                    = 200
	nMax                            = 1
	MaxMovies                       = 5
	MaxSeats                        = 100
	MaxShowings                     = 4
	MaxWindows                      = 2
	runTime           time.Duration = 10 * time.Minute
	summaryReportBase               = "log/theatre.summaryReport."
	ticketServer                    = "http://localhost:1811/tickets"
)

var L *log.Logger

// main starts and runs the model.
// The size and runtime defaults (see const section, above) can be overridden
// by cmd.line options:
//   -?
//   -a <average delay (nDelay)>
//   -c <MaxExchanges>
//   -m <MaxMovies>
//   -s <MaxSeats>
//   -h <MaxShowings>
//   -t <runTime>
//   -w <MaxWindows>
//   -x <nMax>
func main() {

	// This is boilerplate generalized from that in tickets/sample_server.
	// Be nice to find some way to package and import it or something...

	logFileName := logFileBase + time.Now().Format("2006-01-02t15-04-05z-0700")
	logFile, logErr := os.Create(logFileName)
	defer logFile.Close()
	if logErr == nil {
		//// For now, don't run this.  Depending on user's umask, this might
		//// actually INCREASE access to the logfile, instead of protecting it.
		//logErr = logFile.Chmod(0644)
	}
	if logErr != nil {
		// log setup failed, so write this one to the default log, instead
		log.Fatalf("%s aborting:  Error setting up log file '%s':  %v", name, logFileName, logErr)
	}
	L = log.New(logFile, name+":  ", log.Ldate|log.Ltime|log.Lshortfile) //? need '*'

	// End common logging init.

	rand.Seed(time.Now().UnixNano())

	bpHelp := flag.Bool("?", false, "Display this usage info")
	dpAvgDelay := flag.Duration("a", nDelay, "average delay between transactions at the same window (see Go doc for time.ParseDuration)")
	ipExchanges := flag.Int("c", MaxExchanges, "number of exchanges the cafeteria can make before running out of soda (Must match sample_server)")
	ipMovies := flag.Int("m", MaxMovies, "number of movies the theatre can show (Must match sample_server)")
	ipSeats := flag.Int("e", MaxSeats, "number of seats available for each movie showing (Must match sample_server)")
	ipShowings := flag.Int("h", MaxShowings, "number of times each movie is shown, per day (Must match sample_server)")
	dpTime := flag.Duration("t", runTime, "how long to run the model for (see Go doc for time.ParseDuration)")
	ipWindows := flag.Int("w", MaxWindows, "number of open ticket windows (Must match sample_server)")
	ipMax := flag.Int("x", nMax, "maximum number of tickets which may be purchased in one transaction")

	flag.Parse()

	if *bpHelp {
		fmt.Fprintf(os.Stderr, "%s is used to run a theatre model.\nIt expects a tickets server to be running on localhost:1811.\nOptions:\n\n", os.Args[0])
		flag.PrintDefaults()
		os.Exit(2)
	}

	if *dpAvgDelay < 0 {
		L.Fatalf("Startup failed:  -a (average inter-txn delay) must not be negative")
	}

	if *dpTime < 1 {
		L.Fatalf("Startup failed:  -t (time to run the model) must be at least 1ns")
	}

	if *ipMax < 1 {
		L.Fatalf("Startup failed:  -x (max tickets/txn) must be at least 1")
	}

	// initBackend succeeds or dies, no need to return error
	initBackend(*ipExchanges, *ipMovies, *ipShowings, *ipSeats, *ipWindows)

	retCd := 0                                                                                                  // Now that the server is started, need a default return code for stopping it.
	eojMsg := "theatre started the server and is continuing to initialie.\nShutdown not expected at this time." // ... and a default shutdown message.

	chTracker := make(chan interface{}, 5) // All message TO tracker go over this channel (msgTicketSale, msgExchange, and some msgDone)
	chStopWin := make(chan msgStop)        // Used to broadcast shutdown order to ticket windows, by closing the channel, as advised by Donovan & Kernighan, pg 251
	chDone := make(chan interface{})       // Passes msgDone back to main()
	chCafeteria := make(chan xchData, 2)   // Not sure whether buffering is good or bad, here.  Passes xchData to the Cafeteria.  When closed, the Cafeteria knows to close.
	// Since we're not doing customers or actually watching the movies, we
	// don't need a channel for sending tickets or customers from the
	// ticket windows into the theatre spaces.

	// The shutdown process:
	//
	// (Note: golang doesn't really support broadcast messages on channels.
	//        The closest you can come is to close a channel to signal all
	//        of the other users.  But you can't send any information.
	//        There is also no way to query the status of other goroutines,
	//        or to forcibly terminate them.
	//
	//   *  shutdowns are initiated from tracker()
	//   *  first, it closes chStopWin, which connects tracker to the ticket
	//      windows.
	//   *  all of the ticket windows see that, and they terminate.
	//      They send msgDone on chTracker to notify tracker, and
	//      on chDone to notify main().
	//   *  While shutting down, window 1 closes chCafeteria
	//   *  When the Cafeteria notices chCafeteria is closed, then in
	//      it closes, and sends msgDone on chTracker and chDone.
	//   *  When tracker has msgDone from the Cafeteria and all ticket
	//      windows, then tracker prints the summary report, sends
	//      msgDone on chDone to main(), and shuts down.
	//   *  When main has msgDone (on chDone) from all goroutines,
	//      then it shuts down, also.

	go tracker(chTracker, chStopWin, chDone, *dpTime, *ipWindows, *ipMovies, *ipShowings)
	runtime.Gosched() // give the tracker a chance to get started
	go cafeteria(chTracker, chDone, chCafeteria)
	runtime.Gosched() // and give the Cafeteria a chance to get started, also
	for i := 1; i <= *ipWindows; i++ {
		go window(chTracker, chStopWin, chDone, chCafeteria, i, *ipMovies, *ipShowings, *ipMax, *dpAvgDelay)
		// we don't have a customer-provider, so we don't need to wait for the windows to open up
	}

	var iGortns = 1 + 1 + *ipWindows // number of Goroutines we started with = number we're still waiting for
shutdnloop:
	for {
		msg := <-chDone
		L.Printf("SHUTDOWN - Received %T %+v over chDone\n", msg, msg)
		switch msg.(type) {
		case msgDone:
			if iGortns--; iGortns <= 0 {
				break shutdnloop
			}
		// handle other msg types here, if needed
		default: // ignore it
		}
	}
	L.Printf("SHUTDOWN - All Goroutines have exited.  Shutting down server.\n")
	eojMsg = "SHUTDOWN - theatre is shutting down.\nAll Goroutines have exited.\nShutting down server and exiting."
	url := fmt.Sprintf("%s/stop/%d/", ticketServer, retCd)
	L.Printf("SHUTDOWN - main POSTing server shutdown request to %s\n", url)
	response, err := http.Post(url, "text/plain", strings.NewReader(eojMsg))
	L.Printf("SHUTDOWN - main received response:\n%+v\n\n%#v\n", response, response)
	if err != nil {
		L.Printf("SHUTDOWN - main received error:  %v\n", err)
		if retCd == 0 {
			retCd = 40 // indicate remote error.
		}
	}
	os.Exit(retCd)
} // main

// initBackend starts up the backend tickets system.
// Since this is critical to the raison d'etre of theatre,
// it uses fmt.Fatalf to report any failures.
func initBackend(mExch, mMov, mShow, mSeats, mWins int) {
	rqst := struct {
		MaxExchanges int
		MaxMovies    int
		MaxShowings  int
		MaxSeats     int
		MaxWindows   int
	}{mExch, mMov, mShow, mSeats, mWins}

	url := fmt.Sprintf("%s/init/", ticketServer)

	L.Printf("Creating tickets server initialization request:\n%+v\n", rqst)
	// convert rqst to JSON format
	// make HTTP POST request to tickets/init/
	// if successful (HTTP 204),
	//     continue startup
	// if unsuccessful, log it and die
	rqstJSON, err := json.Marshal(rqst)
	if err != nil {
		L.Fatalf("Theatre startup failed:  unable to convert rqst to JSON format:  %v\n", err)
	}
	L.Printf("POSTing initialization request to %s\n", url)
	response, err := http.Post(url, "application/json", bytes.NewReader(rqstJSON))
	L.Printf("Received response to initialization request:\n%+v\n\n%#v\n", response, response)
	switch {
	case err != nil:
		L.Fatalf("Tickets server initialization failed:\n\turl=%s\nerr=%v\n", url, err)
	case response.StatusCode != http.StatusNoContent:
		L.Fatalf("Tickets server initialization failed with status %s\n", response.Status)
	default:
		L.Printf("Tickets server initialization succeeded.  Continuing theatre startup ...\n")
	}
} // initBackend

// tracker is run as a goroutine.
// It tracks the activity of the cafeteria and ticket windows.
// When the user-specified time elapses, it instructs the ticket windows and
// the cafeteria to close.  When everybody has closed, then it generates a
// summary report and closes down.
//
// Parameters:
//
// chTracker
//    The channel which tracker should listen on to receive sales and exchange
//    notifications from the cafeteria and ticket windows.
// chStopWin
//    This channel never carries any actual traffic.  Instead, it is used as a
//    broadcast one-shot (by closing it), to sidgnal ticket windows to close.
// chDone
//    The common channel which all goroutines use to communicate run status.
// runningtime
//    How long the tracker should allow the theatre to be open.
//    It is a time.Duration, and comes from the runTime const or the -t option.
// winctr
//    How many ticket windows were opened.
// movies
//    How many movies there are.
// showings
//    How many showings per day of each movie.
//
// Returns nothing
func tracker(chTracker chan interface{}, chStopWin chan msgStop, chDone chan interface{}, runningtime time.Duration, winctr int, movies int, showings int) {
	if chTracker == nil || chStopWin == nil || chDone == nil || runningtime < 1 || winctr < 1 || movies < 1 || showings < 1 {
		L.Fatalf("tracker() called with invalid parameters:\nchTracker=%v\nchStopWin=%v\nchDone=%v\nrunningtime=%v, winctr=%d, movies=%d, showings=%d\n",
			chTracker, chStopWin, chDone, runningtime, winctr, movies, showings)
	}

	shutdownTimer := time.NewTimer(runningtime)
	var chTrackerOpen = true
	var cafeteriaClosed = false
	var exchangeCtr = 0

	// In each of these tables, the last position in each row and column
	// will be used for totals for each showing and movie, and a grand total
	var ticketsSold = make([][]int, movies+1, movies+1)
	for i, _ := range ticketsSold {
		ticketsSold[i] = make([]int, showings+1, showings+1)
	}
	var soldOuts = make([][]int, movies+1, movies+1)
	for i, _ := range soldOuts {
		soldOuts[i] = make([]int, showings+1, showings+1)
	}

	L.Printf("tracker started ... entering main event/wait loop ...\n")

mainloop:
	for {

		select {
		case s := <-shutdownTimer.C:
			L.Printf("SHUTDOWN - time signal received:  %v  --  notifying ticket windows.\n", s)
			close(chStopWin) // propagate shutdown to all ticket windows.
			shutdownTimer.Stop()
		case x, ok := <-chTracker:
			if !ok {
				break mainloop
			}
			switch x.(type) {
			case msgExchange:
				L.Printf("Processing Exchange notification:  %+v\n", x)
				exchangeCtr++
			case msgTicketSale:
				L.Printf("Processing ticket sales notification:  %+v\n", x)
				for _, t := range x.(msgTicketSale).ticks {
					if t.SoldOut {
						soldOuts[t.Movie][t.Showing]++ // the particular movie and showing
						soldOuts[t.Movie][showings]++  // the movie subtotal
						soldOuts[movies][t.Showing]++  // the showing subtotal
						soldOuts[movies][showings]++   // the grand total
					} else {
						ticketsSold[t.Movie][t.Showing]++ // the particular movie and showing
						ticketsSold[t.Movie][showings]++  // the movie subtotal
						ticketsSold[movies][t.Showing]++  // the showing subtotal
						ticketsSold[movies][showings]++   // the grand total
					}
				}
			case msgDone:
				if strings.Contains(strings.ToLower(x.(msgDone).head.from), "cafeteria") {
					cafeteriaClosed = true
					L.Printf("SHUTDOWN - tracker received msgDone from the cafeteria:  %+v\n", x)
				} else if strings.Contains(strings.ToLower(x.(msgDone).head.from), "window") {
					winctr--
					L.Printf("SHUTDOWN - tracker received msgDone from %+v, %d more ticket windows to go\n", x, winctr)
				} else {
					L.Printf("SHUTDOWN - tracker ignoring msgDone from unknown source:  %+v\n", x)
				}
				if cafeteriaClosed && winctr <= 0 && chTrackerOpen {
					close(chTracker) // and continue in main loop, until chTracker is drained.
					chTrackerOpen = false
				}
			}
		} // select per input event

	} // main loop

	L.Printf("SHUTDOWN - tracker exited main loop.  Printing summary reports and exiting.\n")
	// We are shutting down.  The cafeteria and all of the ticket windows have
	// already shut down.  Time to print the reports and shut down, ourselves.

	summaryReportHead := time.Now().Format("2006-01-02 15:04")
	summaryReportTime := time.Now().Format("2006-01-02t15-04-05z-0700")
	summaryReportName := summaryReportBase + summaryReportTime
	summaryReport, srErr := os.Create(summaryReportName)
	defer summaryReport.Close()
	if srErr == nil {
		//// For now, don't run this.  Depending on user's umask, this might
		//// actually INCREASE access to the logfile, instead of protecting it.
		//srErr = summaryReport.Chmod(0644)
	}
	if srErr != nil {
		// log setup failed, so write this one to the default log, instead
		log.Fatalf("%s aborting:  Error setting up summry report file '%s':  %v", name, summaryReportName, srErr)
	}

	fmt.Fprintf(summaryReport, `Ticket and Exchange Report                             %s

%d Exchanges performed`, summaryReportHead, exchangeCtr)
	summarize(summaryReport, "\nTicket Sales per Movie and Showing", ticketsSold, movies, showings)
	summarize(summaryReport, "\n\nMissed Sales Due to Sellouts per Movie and Showing", soldOuts, movies, showings)

	chDone <- msgDone{head: msgHeader{at: time.Now(), from: "tracker"}}
	//runtime.Goexit   ---   getting strange error "runtime.Goexit evaluated but not used"

} // tracker

// summarize(outfile, tableHead, tickTable, movies, showings)
// Formats and prints the tickTable as a (movies+1) x (showings+1) matrix of Movies and Showings.
// The table is "printed" to the outfile, along with the supplied heading.
func summarize(outfile *os.File, tableHead string, tickTable [][]int, movies int, showings int) {
	fmt.Fprintf(outfile, "%s\n             ", tableHead) // Blanks after the NL = len("All showings ")
	for i := 0; i < movies; i++ {
		fmt.Fprintf(outfile, "Movie %2d  ", i) // Do NOT use a newline here!
	}
	fmt.Fprintln(outfile, "All movies")
	for j := 0; j <= showings; j++ {
		if j == showings {
			fmt.Fprintf(outfile, "All showings ") // NO NL
		} else {
			fmt.Fprintf(outfile, "Showing %2d   ", j) // NO NL
		}
		for i := 0; i <= movies; i++ {
			fmt.Fprintf(outfile, "%8d  ", tickTable[i][j])
		}
		fmt.Fprintln(outfile, "")
	}
} // summarize

// cafeteria models the theatre's cafeteria.  It is run as a Goroutine.
// In the initial implementation, all it does is perform exchanges of free
// water for soda, using the tickets system, and notify the tracker when
// it has performed such an exchange.
//
// It responds to a msgStop with what="cafeteria" on the chDone channel by
// shutting down.
//
// Parameters
//
// chTracker
//    The channel which the Cafeteria should use to notify the tracker
//    that an exchange has been performed.
//    Also sends msgDone on chTracker, to inform tracker that it is closing.
// chDone
//    Sends msgDone on chDone to inform main() that it is closing down.
// chCafeteria
//    The channel which the Cafeteria should listen, for exchange requests
//    being sent over by the ticket window(s).  The cafeteria does not need
//    to know the window rules, because the tickets engine will determine
//    whether the request is valid or not.
//    When this channel is closed by a ticket window, it inidates that the
//    Cafeteria should close down.
//
// Returns nothing
func cafeteria(chTracker chan interface{}, chDone chan interface{}, chCafeteria chan xchData) {

	// The only possible exchange right now is water for soda:
	var exchangeold = "water"
	var exchangenew = "soda"

	L.Printf("cafeteria started ... entering main event/wait loop ...\n")

	for {

		select {
		case x, ok := <-chCafeteria:
			if !ok {
				L.Printf("SHUTDOWN - chCafeteria has been closed and drained.  Shutting down.\n")
				chTracker <- msgDone{head: msgHeader{at: time.Now(), from: "cafeteria"}} // tell tracker()
				chDone <- msgDone{head: msgHeader{at: time.Now(), from: "cafeteria"}}    // tell main()
				runtime.Goexit()
			}
			L.Printf("Cafeteria received exchange request:  %+v\n", x)
			// make HTTP request to tickets/exchange/<tickNum>/water/soda
			// if successful (HTTP 204), send a msgExchange to tracker
			// if unsuccessful, log it and continue
			url := fmt.Sprintf("%s/exchange/%d/%s/%s/", ticketServer, x.tickNum, exchangeold, exchangenew)
			L.Printf("cafeteria GETing exchange from %s\n", url)
			response, err := http.Get(url)
			if err != nil {
				L.Printf("Cafeteria exchange failed:\n\turl=%s\nerr=%v\n", url, err)
			} else if response.StatusCode == http.StatusNoContent {
				L.Printf("Cafeteria exchange succeeded.  Notifying tracker ...\n")
				chTracker <- msgExchange{head: msgHeader{at: time.Now(), from: "cafeteria"}, tickNum: x.tickNum, xchOld: exchangeold, xchNew: exchangenew}
				L.Printf("Cafeteria exchange notification sent.\n")
			} else {
				L.Printf("Cafeteria exchange denied by tickets server with status %s\n", response.Status)
			}
		} // select per input event
	} // main event/wait loop

	// Should not be able to get here.

} // cafeteria

// window models a ticket window.  It is run as a Goroutine.
// In the initial implementation,it sells a random number of tickets for random
// movies and showings, using the tickets system, and notifies the tracker when
// it has performed the salse.  If this instance of window happens to be window
// 1, then it also selects a random subset of the sold tickets to go to the
// Cafeteria and exchange their water for soda.  The Cafeteria is responsible
// for notifying the tracker of successful exchanges.
//
// It responds to a msgStop with what="window" on the chDone channel by shutting
// down.
//
// Parameters
//
// chTracker
//    The channel which the window should use to notify the tracker
//    that a sale has been processed (informs of both sold tickets and
//    unable-to-sell-because-sold-out ticket requests).
//    Also sends msgDone on chTracker, to inform tracker that it is closing.
// chStopWin
//    This channel never carries any actual traffic.  Instead, it is used as a
//    broadcast one-shot (when tracker closes it), to signal the ticket windows
//    to close.
// chDone
//    Sends msgDone on chDone to inform main() that it is closing down.
// chCafeteria
//    The channel which the window should use to send exchange requests
//    to the Cafeteria.
//    When the window shuts down, it also sends a msgDone to notify the
//    Cafeteria that it should also shut down.
// iWindow
//    This window's Window number.  Window 1 is special, because only it is
//    authorized to give out promotional goodies, and to direct interested
//    customers to the Cafeteria to exchange them.  Assumed to be between 1
//    and *ipWindows.
// iMovies
//    The number of movies available at the theatre, numbered 0 to iMovies-1.
//    Assumed to be at least 1.
// iShowings
//    The number of showings of each movie available at the theatre, numbered
//    0 to iShowings-1.  Assumed to be at least 1.
// iMax
//    The maximum number of tickets the customer is allowed to buy.
//    Assumed to be at least 1.
// dAvgDelay
//    A time.Duration suggesting the average delay between transactions at
//    the window.  The actual delay between transactions is random, between
//    none, and twice dAvgDelay, except that if dAvgDelay is 0, then no
//    artificial delays are introduced.  Set to 0, if negative.
//
// Returns nothing
func window(chTracker chan interface{}, chStopWin chan msgStop, chDone chan interface{}, chCafeteria chan xchData, iWindow int, iMovies int, iShowings int, iMax int, dAvgDelay time.Duration) {

	// Configure random delays averaging dAvgDelay.
	// Not sure that this is the best way to do this, because it assumes
	// knowledge that type Duration is derived from int64.
	if dAvgDelay < 0 {
		dAvgDelay = 0
	}
	var randlimit int64

	L.Printf("window %d started ... entering main event/wait loop ...\n", iWindow)

	for { // process timers, sales, and interrupts until told to stop

		switch {
		case dAvgDelay == 0: // do nothing - no delays
		case randlimit == 0: // first time thru - configure delay
			randlimit = 2*(int64(dAvgDelay)) + 1
		default: // between passes - wait a bit
			time.Sleep(time.Duration(rand.Int63n(randlimit)))
		}

		makeSale(chTracker, chCafeteria, iWindow, iMovies, iShowings, iMax) // makeSale responsible for error handling/logging

		select {
		case m, ok := <-chStopWin:
			if !ok {
				L.Printf("SHUTDOWN - chStopWin has been closed and drained.  Shutting down window %d.\n", iWindow)
				chTracker <- msgDone{head: msgHeader{at: time.Now(), from: "window"}} // tell tracker()
				chDone <- msgDone{head: msgHeader{at: time.Now(), from: "window"}}    // tell main()
				if iWindow == 1 {
					close(chCafeteria)
					L.Printf("SHUTDOWN - window %d closed chCafeteria.  The Cafeteria should beging shutting down now.", iWindow)
				}
				runtime.Goexit()
			}
			L.Printf("SHUTDOWN - Unexpected message type %T ignored by window %d on chStopWin:  %+v\n", m, iWindow, m)
		default:
		} // select per input event
	} // main loop

	// Should not be able to get here.

} // window

// makeSale performs the actual sale at a ticket window.
// It generates random numbers to:
//   *  determine how many different tickets to buy
//      (each of the following is done separately for each ticket)
//   *  choose among 5 movies (or whatever iMovies is)
//   *  choose among 4 showings (or whatever iShowings is)
//   *  decide whether to exchange the promo goodies (Window 1 only)
//
// Parameters
//
// chTracker
//    The channel which the window should use to notify the tracker
//    that a sale has been performed (although it is possible that no
//    tickets were actually sold [i.e. the notification could contain
//    only sold-out placeholders], if all of the requested showings were
//    sold out).
// chCafeteria
//    The channel which the window should use to send exchange requests
//    to the Cafeteria.
// iWindow
//    This window's Window number.  Window 1 is special, because only it is
//    authorized to give out promotional goodies, and to direct interested
//    customers to the Cafeteria to exchange them.  Assumed to be between 1
//    and *ipWindows.
// iMovies
//    The number of movies available at the theatre, numbered 0 to iMovies-1.
//    Assumed to be at least 1.
// iShowings
//    The number of showings of each movie available at the theatre, numbered
//    0 to iShowings-1.  Assumed to be at least 1.
// iMax
//    The maximum number of tickets the customer is allowed to buy.
//    Assumed to be at least 1.
func makeSale(chTracker chan interface{}, chCafeteria chan xchData, iWindow int, iMovies int, iShowings int, iMax int) {
	L.Printf("makeSale(chTracker,chCafeteria,iWindow=%d,iMovies=%d,iShowings=%d,iMax=%d) called.\n",
		iWindow, iMovies, iShowings, iMax)
	url := fmt.Sprintf("%s/sell/%d/", ticketServer, iWindow)
	items := 1 + rand.Intn(iMax) // number of items which will be purchased, if they're not sold out already
	rqst := make(map[string]interface{})
	rqst["LocalTime"] = time.Now()
	rqst["PaymentInfo"] = map[string]interface{}{"Reserved": "PaymentInfo is reserved for future use."}
	rqst["TicketRequests"] = make([][2]int, items, items)

	for i := 0; i < items; i++ {
		thisMovie := rand.Intn(iMovies)     // movie# indexing is 0-based, rather than 1-based
		thisShowing := rand.Intn(iShowings) // showing# indexing is 0-based, rather than 1-based
		rqst["TicketRequests"].([][2]int)[i] = [2]int{thisMovie, thisShowing}
	}

	L.Printf("makeSale for window %d has generated request:\n%+v\n", iWindow, rqst)
	// convert rqst to JSON format
	// make HTTP POST request to tickets/sell/<windowNumber>
	// if successful (HTTP 200),
	//     send a msgTicketSale to tracker
	//     determine which to send to the Cafeteria for exchange, and do so
	// if unsuccessful, log it and continue
	rqstJSON, err := json.Marshal(rqst)
	if err != nil {
		L.Printf("makeSale for window %d failed:  unable to convert rqst to JSON format:  %v\n", iWindow, err)
		return
	}
	L.Printf("makeSale for window %d POSTing ticket requests to %s\n", iWindow, url)
	response, err := http.Post(url, "application/json", bytes.NewReader(rqstJSON))
	L.Printf("makeSale for window %d received response:\n%+v\n\n%#v\n", iWindow, response, response)
	if err != nil {
		L.Printf("makeSale for window %d failed:  sell service failed:  \n\turl=%s\nerr=%v\n", iWindow, url, err)
		return
	} else if response.StatusCode == http.StatusOK {
		var responseData struct {
			// All fields must be exported (capitalized), to be visible to json.
			Ticks []tickets.Ticket
			Rcpt  tickets.Receipt
		}
		jbytes, err := ioutil.ReadAll(response.Body)
		if err != nil {
			L.Printf("makeSale for window %d failed:  cannot read sell service call's response.Body:  %v\n", iWindow, err)
			return
		}

		jbuffer := bytes.NewBuffer(jbytes)
		L.Printf("makeSale for window %d received %d bytes of raw response.Body:\n%s\n", iWindow, len(jbytes), jbuffer.String())
		jparser := json.NewDecoder(jbuffer)
		//jparser := json.NewDecoder(response.Body)
		defer response.Body.Close()
		if err := jparser.Decode(&responseData); err != nil {
			L.Printf("makeSale for window %d failed:  sell service call reported status OK but response data not in JSON format:  %v\n", iWindow, url, err)
			return
		}
		L.Printf("makeSale for window %d sell service call succeeded.  Notifying tracker ...\n", iWindow)
		chTracker <- msgTicketSale{head: msgHeader{at: time.Now(), from: "window"}, window: iWindow, ticks: responseData.Ticks}
		L.Printf("makeSale for window %d tracker notification sent.\n", iWindow)
		L.Printf("makeSale for window %d sell service call succeeded.  Receipt:\n%+v\nTickets:\n", iWindow, responseData.Rcpt)
		for _, t := range responseData.Ticks {
			L.Printf("\tticket:  %+v\n", t)
			if t.Goodies {
				exchangeIt := rand.Intn(10)%2 == 0 // even -> true = try to exchange the water, odd -> false = keep it
				if exchangeIt {
					x := xchData{head: msgHeader{at: time.Now(), from: "window " + strconv.Itoa(iWindow)}, tickNum: t.TicketNum}
					chCafeteria <- x
					L.Printf("\t\t(exchange sent:  %+v)\n", x)
				} else {
					L.Printf("\t\t(not exchanged)\n")
				}
			} else {
				L.Printf("\t\t(no goodies to consider exchanging)\n")
			}
		}
	} else {
		L.Printf("makeSale for window %d sell service call failed with status %s.  Sale abandoned.\n", iWindow, response.Status)
		return
	}

	return
} // makeSale

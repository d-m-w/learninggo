/*****************************************************************************

'tickets' sells movie tickets and manages promotional gifts ("goodies").

This program is an optional http server wrapped around the 'tickets' package.
You can use it if you want to make 'tickets' available as a stand-alone Web
service.  It will then have a JSON interface.

The following URLs are supported:
    /tickets/sell/<window_number>
        This URL is accessed with POST.  The request is sent in JSON format:
            {
                "TicketRequests" : [ [ <movie#>, <showning#> ], ... ],
                "PaymentInfo"    : <can be anything -- validation not implemented>,
                "LocalTime"      : <can be anything -- not currently used>
            }
        The reply is also sent back in JSON format:
            {
                "tickets"        :   [ { <struct Ticket expressed as a JSON map> }, ... ],
                "receipt"        :   { <struct Receipt expressed as a JSON map> }
            }
	and you get HTTP 200 on success.
    /tickets/exchange/<ticket_number>/<old_goodie>/<new_goodie>
        There is no additional payload with this URL.  Use GET or POST.
        There is no reply data (get HTTP 204 on success).

See the doc. in tickets.go for application details.

*****************************************************************************/

package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/d-m-w/learninggo/tickets"
	//"tickets"
)

const (

	// ATTENTION!  constants named Max* are shared with theatre and must be kept in sync.
	//             If overridden by a cmd.line option, then the SAME option must be given
	//             to theatre when it is started.  Unspeakable horrors may result,
	//             elsewise.

	ServerPort = "1811" // for tickets service on localhost

	MaxExchanges = 200 // items available for exchange
	MaxMovies    = 5   // in the theatre
	MaxShowings  = 4   // per movie
	MaxSeats     = 100 // per movie showing
	MaxWindows   = 2   // for selling tickets

	LogFileBase = "log/tickets."
)

var L *log.Logger

// main starts and runs the sample tickets server.
// The size and runtime defaults (see const section, above) can be overridden
// by cmd.line options:
//   -c <MaxExchanges>
//   -m <MaxMovies>
//   -s <MaxSeats>
//   -h <MaxShowings>
//   -w <MaxWindows>
func main() {
	logFileName := LogFileBase + time.Now().Format("2006-01-02t15-04-05z-0700")
	logFile, logErr := os.Create(logFileName)
	defer logFile.Close()
	if logErr == nil {
		//// For now, don't run this.  Depending on user's umask, this might
		//// actually INCREASE access to the logfile, instead of protecting it.
		//logErr = logFile.Chmod(0644)
	}
	if logErr != nil {
		// log setup failed, so write this one to the default log, instead
		log.Fatalf("ticketServer aborting:  Error setting up log file '%s':  %v", logFileName, logErr)
	}
	L = log.New(logFile, "ticketServer:  ", log.Ldate|log.Ltime|log.Lshortfile) //? need '*'

	// End common logging init.

	ipExchanges := flag.Int("c", MaxExchanges, "number of exchanges the cafeteria can make before running out of soda (Must match theatre model)")
	ipMovies := flag.Int("m", MaxMovies, "number of movies the theatre can show (Must match theatre model)")
	ipSeats := flag.Int("e", MaxSeats, "number of seats available for each movie showing (Must match theatre model)")
	ipShowings := flag.Int("h", MaxShowings, "number of times each movie is shown, per day (Must match theatre model)")
	ipWindows := flag.Int("w", MaxWindows, "number of open ticket windows (Must match theatre model)")

	flag.Parse()

	if err := tickets.Init(L, *ipExchanges, *ipMovies, *ipShowings, *ipSeats, *ipWindows); err != nil {
		L.Fatalf("Startup failed:  ticket system initialization failed:  %v\n", err)
	}

	http.HandleFunc("/tickets/sell/", sellTickets)
	http.HandleFunc("/tickets/exchange/", handleExchange)
	L.Fatal(http.ListenAndServe("localhost:"+ServerPort, nil))
} // main

// handleExchange is an adapter between the http Handler protocol and the
// ticketing system's Exchange function.  The URL format is:
//     /tickets/exchange/<ticket_number>/<old_goodie>/<new_goodie>
// and there is no additional response body.  Access the URL with HTTP GET.
//
// If there are no errors in the request and the specified ticket allows the
// exchange, then the specified item is reported as exchanged for the specified
// replacement, in the ticketing system.
//
// Returns HTTP 400 on errors and HTTP 204 on success.
// No payload is ever sent back.
func handleExchange(w http.ResponseWriter, rqst *http.Request) {
	const (
		PPTickNum   = 3 // where's the ticket number in the URL.Path?
		PPOldGoodie = 4 // where's the goodie to be exchanged, in the URL.Path?
		PPNewGoodie = 5 // where's the requested replacement item, in the URL.Path?
	)

	L.Printf("handleExchange called for %v\n", rqst.URL)

	pathParts := strings.Split(rqst.URL.Path, "/")
	tickNum, err := strconv.Atoi(pathParts[PPTickNum])
	if err != nil {
		L.Printf("Request '%s' failed:  ticket number invalid:  %v\n", rqst.URL.Path, err)
		http.Error(w, "ticket number invalid", http.StatusBadRequest)
		return
	}

	err = tickets.Exchange(tickNum, pathParts[PPOldGoodie], pathParts[PPNewGoodie])
	if err != nil {
		L.Printf("Request '%s' failed:  error from tickets.Exchange:  %v\n", rqst.URL.Path, err)
		http.Error(w, fmt.Sprintf("%v", err), http.StatusBadRequest)
		return
	}

	// I don't think calling "Error" is really the right way to report success,
	// but I can't find any other way to send back a 204 ...
	http.Error(w, "exchanged", http.StatusNoContent)
	return
} // handleExchange

// sellTickets is an adapter between the http Handler protocol and the
// ticketing system's Sell function.
//
// URLs are accessed with HTTP POST.  URL format
//   /tickets/sell/<window_number>
//
// JSON data format:
//   {
//     "TicketRequests" : [ [<movie#>, <showing#>], [<movie#>, <showing#>], ... ],
//     "PaymentInfo"    : { <any number of fields with any contents> },
//     "LocalTime"      : <anything>
//   }
//
// One possible GO data format:
//   var requestData struct {
//      // Use the same case for the variable names as the JSON map keys.
//      TicketRequests [][2]int               // { movie #, showing # }
//      PaymentInfo    map[string]interface{} // not currently implemented
//      LocalTime      interface{}            // not currently implemented
//   }
//
// If there are no errors, then the Sell function's response converted to JSON
// format and returned, with an HTTP 200 status code.
//
// If an error occurs, then HTTP 400 or 500 is returned.
func sellTickets(w http.ResponseWriter, rqst *http.Request) {
	const (
		PPWindow = 3 // where's the Window# in the URL.Path?
	)
	var requestData struct {
		// Use the same case for the variable names as the JSON map keys.
		TicketRequests [][2]int               // { movie #, showing # }
		PaymentInfo    map[string]interface{} // not currently implemented
		LocalTime      interface{}            // not currently implemented
	}

	L.Printf("sellTickets called for %v\n", rqst.URL)

	jparser := json.NewDecoder(rqst.Body)

	if err := jparser.Decode(&requestData); err != nil {
		L.Printf("Request '%s' failed:  data not in JSON format:  %v\n", rqst.URL.Path, err)
		http.Error(w, "data not in JSON format", http.StatusBadRequest)
		return
	}

	window, winerr := strconv.Atoi(strings.Split(rqst.URL.Path, "/")[PPWindow])
	if winerr != nil {
		L.Printf("Request '%s' failed:  window number invalid:  %v\n", rqst.URL.Path, winerr)
		http.Error(w, "window number invalid", http.StatusBadRequest)
		return
	}

	ticks, rcpt, err := tickets.Sell(window, requestData.TicketRequests, requestData.PaymentInfo, requestData.LocalTime)
	if err != nil {
		L.Printf("Request '%s' failed:  error from tickets.Sell:  %v\n", rqst.URL.Path, err)
		http.Error(w, fmt.Sprintf("%v", err), http.StatusBadRequest)
		return
	}

	var responseData struct {
		// All fields must be exported (capitalized), to be visible to json.
		Ticks []tickets.Ticket
		Rcpt  tickets.Receipt
	}
	responseData.Ticks = ticks
	responseData.Rcpt = rcpt
	L.Printf("sellTickets window %d responseData\n%+v\n", window, responseData)
	//jcoder := json.NewEncoder(w)
	//if err := jcoder.Encode(responseData); err != nil {
	jbuffer, err := json.Marshal(responseData)
	if err != nil {
		L.Printf("Request '%s' failed:  error marshalling response data to JSON:  %v\n", rqst.URL.Path, err)
		http.Error(w, "error marshalling response data to JSON", http.StatusInternalServerError)
		return
	}
	L.Printf("Marshalled responseData is %d bytes:\n'%s'\n", len(jbuffer), bytes.NewBuffer(jbuffer).String())
	w.Write(jbuffer)
	// Note:  http.ResponseWriter doesn't have a Close() method, so can't do that.
	return
} // sellTickets

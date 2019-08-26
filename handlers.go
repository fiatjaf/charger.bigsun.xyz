package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"

	"github.com/elazarl/go-bindata-assetfs"
	"github.com/fiatjaf/go-lnurl"
	"gopkg.in/antage/eventsource.v1"
)

func setupHandlers() {
	http.HandleFunc("/lnurl-withdraw", func(w http.ResponseWriter, r *http.Request) {
		session := r.URL.Query().Get("session")
		pubkey := userKeys[session]
		label := "inv-espera-" + pubkey

		// query invoice on spark
		resp, err := spark.Call("listinvoices", label)
		if err != nil {
			log.Error().Err(err).Str("session", session).Str("key", pubkey).
				Msg("failed to listinvoices on lnurl-withdraw first call")
			w.Header().Add("Content-Type", "application/json")
			json.NewEncoder(w).Encode(
				lnurl.ErrorResponse("Invalid response from node. Please report if this persists."),
			)
			return
		}
		msatoshi := resp.Get("invoices.0.msatoshi").Int()

		json.NewEncoder(w).Encode(lnurl.LNURLWithdrawResponse{
			LNURLResponse:      lnurl.LNURLResponse{Status: "OK"},
			Callback:           fmt.Sprintf("%s/lnurl-withdraw/callback/%s", s.ServiceURL, session),
			K1:                 randomHex(64), // use a new k1 here just because we can
			MaxWithdrawable:    msatoshi,
			MinWithdrawable:    msatoshi,
			DefaultDescription: "charger.alhur.es withdraw",
			Tag:                "withdrawRequest",
		})
	})

	http.HandleFunc("/lnurl-withdraw/callback/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		session := parts[len(parts)-1]
		pubkey := userKeys[session]
		label := "inv-espera-" + pubkey

		// check signature
		k1 := r.URL.Query().Get("k1")
		sig := r.URL.Query().Get("sig")
		if ok, err := lnurl.VerifySignature(k1, sig, pubkey); !ok {
			log.Warn().Err(err).Msg("withdraw signature verification failed")
			json.NewEncoder(w).Encode(lnurl.ErrorResponse("Invalid signature!"))
			return
		}

		// check amounts
		resp, err := spark.Call("listinvoices", label)
		if err != nil {
			log.Error().Err(err).Str("session", session).Str("key", pubkey).
				Msg("failed to listinvoices on lnurl-withdraw second call")
			w.Header().Add("Content-Type", "application/json")
			json.NewEncoder(w).Encode(
				lnurl.ErrorResponse("Invalid response from node. Please report if this persists."),
			)
			return
		}
		msatoshiBalance := resp.Get("invoices.0.msatoshi").Int()

		pr := r.URL.Query().Get("pr")
		resp, err = spark.Call("decodepay", pr)
		if err != nil {
			log.Error().Err(err).Str("session", session).Str("key", pubkey).Str("pr", pr).
				Msg("invalid invoice received")
			w.Header().Add("Content-Type", "application/json")
			json.NewEncoder(w).Encode(lnurl.ErrorResponse("The invoice we got is invalid."))
			return
		}
		msatoshiInvoice := resp.Get("msatoshi").Int()

		if msatoshiBalance != msatoshiInvoice {
			log.Error().Err(err).Str("session", session).Str("key", pubkey).Str("pr", pr).Msg("wrong amount invoice")
			w.Header().Add("Content-Type", "application/json")
			json.NewEncoder(w).Encode(lnurl.ErrorResponse("The invoice we got has the wrong value."))
			return
		}

		// check lock
		if inprocess, ok := withdrawalsInProcess[label]; ok && inprocess {
			log.Warn().Str("key", pubkey).Msg("duplicated withdraw attempt prevented")
			json.NewEncoder(w).Encode(lnurl.ErrorResponse("Can't withdraw twice!"))
			return
		}

		// everything is ok, process withdraw
		go func() {
			withdrawalsInProcess[label] = true
			_, err := spark.Call("waitpay", pr)
			if err != nil {
				log.Error().Err(err).Str("key", pubkey).Str("pr", pr).Msg("failed to pay")
			}

			// delete invoice from spark (so the user can do it again)
			_, err = spark.Call("delinvoice", label, "paid")
			if err != nil {
				log.Error().Err(err).Str("label", label).Msg("failed to delete invoice after payment")
			}
			delete(withdrawalsInProcess, label)

			if es, ok := userStreams[session]; ok {
				es.SendEventMessage(`{"processed": true}`, "withdraw", "")
			}
		}()

		if es, ok := userStreams[session]; ok {
			es.SendEventMessage(`{"processing": true}`, "withdraw", "")
		}
		json.NewEncoder(w).Encode(lnurl.OkResponse())
	})

	http.HandleFunc("/cancel-invoice", func(w http.ResponseWriter, r *http.Request) {
		session := r.URL.Query().Get("session")
		pubkey := userKeys[session]
		label := "inv-espera-" + pubkey

		_, err = spark.Call("delinvoice", label, "unpaid")
		if err != nil {
			log.Error().Err(err).Str("label", label).Msg("failed to delete invoice on cancel")
			return
		}

		// this should reset the state on the client
		if es, ok := userStreams[session]; ok {
			es.SendEventMessage(`{"waiting": false}`, "withdraw", "")
			es.SendEventMessage(`null`, "btc-deposit", "")
		}
	})

	http.HandleFunc("/invoice-intent", func(w http.ResponseWriter, r *http.Request) {
		amountSats := r.FormValue("amount")
		session := r.FormValue("session")
		pubkey := userKeys[session]
		label := "inv-espera-" + pubkey

		log.Debug().Str("key", pubkey).Str("amount", amountSats).Msg("invoice-intent")

		// generate invoice on spark
		inv, err := spark.Call("invoice", amountSats+"000", label, label, 1204800)
		if err != nil {
			log.Error().Err(err).Str("amount", amountSats).Str("pubkey", pubkey).Msg("failed to get invoice")
			return
		}
		bolt11 := inv.Get("bolt11").String()

		// call golightning.club
		var glresp struct {
			Address string `json:"bitcoinAddress"`
			Price   string `json:"btcPrice"`
			Error   string `json:"error"`
		}
		resp, err := http.PostForm("https://api.golightning.club/new", url.Values{"bolt11": {bolt11}})
		if err != nil {
			log.Error().Msg("golightning.club request failed")
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			b, _ := ioutil.ReadAll(resp.Body)
			log.Error().Str("body", string(b)).Msg("golightning.club request failed")
			return
		}
		err = json.NewDecoder(resp.Body).Decode(&glresp)
		if err != nil {
			b, _ := ioutil.ReadAll(resp.Body)
			log.Error().Str("body", string(b)).Msg("failed to parse golightning.club response")
			return
		}
		if glresp.Error != "" {
			log.Error().Str("err", glresp.Error).Str("bolt11", bolt11).Msg("golightning.club returned error")
			return
		}

		// finally we have the data we need
		if es, ok := userStreams[session]; ok {
			es.SendEventMessage(
				fmt.Sprintf(`{"address": "%s", "price": "%s"}`, glresp.Address, glresp.Price),
				"btc-deposit",
				"",
			)
		}
	})

	http.HandleFunc("/get-params", func(w http.ResponseWriter, r *http.Request) {
		session := randomHex(64)
		lnurl, err := lnurl.LNURLEncode(fmt.Sprintf("%s/lnurl-login?tag=login&k1=%s", s.ServiceURL, session))
		if err != nil {
			log.Error().Err(err).Msg("failed to encode lnurl")
			return
		}

		w.Header().Add("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			Session string `json:"session"`
			LNURL   string `json:"lnurl"`
		}{session, lnurl})
	})

	http.HandleFunc("/user-data", func(w http.ResponseWriter, r *http.Request) {
		session := r.URL.Query().Get("session")
		es, ok := userStreams[session]

		if !ok {
			es = eventsource.New(
				eventsource.DefaultSettings(),
				func(r *http.Request) [][]byte {
					return [][]byte{
						[]byte("X-Accel-Buffering: no"),
						[]byte("Cache-Control: no-cache"),
						[]byte("Content-Type: text/event-stream"),
						[]byte("Connection: keep-alive"),
						[]byte("Access-Control-Allow-Origin: *"),
					}
				},
			)

			userStreams[session] = es
		}

		es.ServeHTTP(w, r)
	})

	http.HandleFunc("/lnurl-login", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.String(), "?")
		actualQS := parts[len(parts)-1] // last ? segment
		params, err := url.ParseQuery(actualQS)
		if err != nil {
			log.Print("borked querystring " + r.URL.String() + ": " + err.Error())
			return
		}

		k1 := params.Get("k1")
		sig := params.Get("sig")
		key := params.Get("key")

		if ok, err := lnurl.VerifySignature(k1, sig, key); !ok {
			log.Warn().Err(err).Msg("initial signature verification failed")
			return
		}

		session := k1
		log.Debug().Str("session", session).Str("pubkey", key).Msg("valid login")
		userKeys[session] = key

		// if there's an active login SSE stream for this, notify there
		if es, ok := userStreams[session]; ok {
			es.SendEventMessage(`"`+key+`"`, "login", "")

			// check if there's a pending withdraw for this user
			listinv, err := spark.Call("listinvoices", "inv-espera-"+key)
			if err != nil {
				log.Error().Err(err).Msg("spark listinvoices call error")
			}
			if listinv.Get("invoices.#").Int() > 0 {
				// there is an invoice
				inv := listinv.Get("invoices.0")
				switch inv.Get("status").String() {
				case "paid":
					// withdraw available
					lnurl, err := lnurl.LNURLEncode(fmt.Sprintf("%s/lnurl-withdraw?session=%s", s.ServiceURL, session))
					if err != nil {
						log.Error().Err(err).Msg("failed to encode lnurl")
						return
					}
					es.SendEventMessage(fmt.Sprintf(`{"ready": true, "lnurl": "%s"}`, lnurl), "withdraw", "")
				case "unpaid":
					// tell the user we're waiting for the payment to come in,
					// allow him to cancel and start anew
					es.SendEventMessage(`{"waiting": true}`, "withdraw", "")
				}
			} else {
				// there is nothing
				es.SendEventMessage(`{"waiting": false}`, "withdraw", "")
			}
		}

		json.NewEncoder(w).Encode(lnurl.LNURLResponse{Status: "OK"})
	})

	http.Handle("/", http.FileServer(&assetfs.AssetFS{Asset: Asset, AssetDir: AssetDir, Prefix: "/static/"}))
}

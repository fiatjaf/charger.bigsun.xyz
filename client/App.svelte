<script>
import { onMount } from 'svelte';
import QR from './QR.svelte'

var params = null
var loggedkey = null
var amountSats = 100000
var golightning = null
var withdraw = null

onMount(async () => {
  let r = await fetch('/get-params')
  params = await r.json()

  var es = new EventSource('/user-data?session=' + params.session)
  es.addEventListener('login', e => { loggedkey = JSON.parse(e.data) })
  es.addEventListener('btc-deposit', e => { golightning = JSON.parse(e.data) })
  es.addEventListener('withdraw', e => { withdraw = JSON.parse(e.data) })
  es.addEventListener('end', e => { ended = JSON.parse(e.data) })
})

async function submitAmount (e) {
  e.preventDefault()

  let body = new FormData()
  body.append('amount', amountSats)
  body.append('session', params.session)

  let r = await fetch('/invoice-intent', {
    method: 'post',
    body
  })
  let data = await r.json()

  address = data.address
}

async function cancelInvoice (e) {
  e.preventDefault()
  fetch('/cancel-invoice?session=' + params.session)
}
</script>

<style>
  #main {
    margin: 23px auto;
    width: 700px;
  }

  span.cancel {
    cursor: pointer;
    color: #333;
    background: #ddd;
  }

  span.cancel:hover { background: #bbb; }
</style>

<div id="main">
  <h1>Lightning Charger</h1>
  {#if params && !loggedkey}
    <a href="lightning:{params.lnurl}"><QR value={params.lnurl} color="#075d75" /></a>
    <p>Scan the code above with <a href="https://lightning-wallet.com/">BLW</a> or other <a href="https://github.com/btcontract/lnurl-rfc/blob/master/spec.md#2-lnurl-login">lnurl</a>-compatible wallet.</p>
  {/if}
  {#if loggedkey}
    <p>Logged as <code>{loggedkey}</code>.</p>

    {#if golightning === null && withdraw && withdraw.waiting === false}
      <p>Now type an amount to be transferred from your Bitcoin on-chain wallet to your Lightning wallet.</p>
      <form on:submit={submitAmount}>
        <label>Amount in satoshis:
          <input type="number" bind:value={amountSats} min="10000" max="3000000">
        </label>
        <button>Done</button>
      </form>
      <p><small>99 satoshis will be added, it's the <a href="https://golightning.club/" target="_blank">golightning.club</a> service fee (the smallest in the market), and you'll have to submit <code>{(amountSats + 99) / 100000000} BTC</code> on-chain.</small></p>
    {:else if golightning}
      <p>Charge request submitted.</p>
      <p>You can now send <code>{golightning.price} BTC</code> to the address below. You have 10 days for your transaction to be confirmed. After that you can come back here at any time and withdraw your satoshis (even from different browsers, nothing is stored).</p>
      <a href="bitcoin:{golightning.address}?amount={golightning.price}"><QR value={golightning.address + '?amount=' + golightning.price} color="#b1670e" /></a>
      <p><code><a href="bitcoin:{golightning.address}?amount={golightning.price}">{golightning.address}</a></code></p>
      <p>If everything is wrong <span class="cancel" on:click={cancelInvoice}>click here</span> to cancel (don't click if you've already sent your Bitcoin transaction).</p>
    {:else if withdraw && withdraw.waiting === true}
      <p>We're waiting for your <b>Bitcoin transaction</b> to be confirmed so you can withdraw your satoshis to your Lightning wallet. However if you believe this is an error, please contact us on <a href="https://t.me/fiatjaf">Telegram</a> or <a href="https://twitter.com/fiatjaf">Twitter</a>.</p>
      <p>If you didn't send a Bitcoin transaction and just want to start everything from scratch <span class="cancel" on:click={cancelInvoice}>click here</span>.</p>
    {:else if withdraw && withdraw.ready === true}
      <p>Your satoshis are available for withdraw.</p>
      <a href="lightning:{withdraw.lnurl}"><QR value={withdraw.lnurl} color="#077510" /></a>
      <p>Scan the code above to withdraw.</p>
    {:else if withdraw && withdraw.processing  === true}
      <p>Transaction being sent to your wallet.</p>
    {:else if withdraw && withdraw.processed === true}
      <p>Transaction sent!</p>
    {/if}
  {/if}
</div>

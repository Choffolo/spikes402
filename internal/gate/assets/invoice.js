(function(){
var box=document.getElementById("pay-wait");
if(!box)return;
var token=box.getAttribute("data-token"),exp=+box.getAttribute("data-expires")*1000,claim=box.getAttribute("data-claim")||"/.spikes402/claim";
var msg=document.getElementById("pay-msg"),timer,stopped=false;
box.hidden=false;
function say(t,err){if(msg){msg.textContent=t||"";msg.className=err?"err":""}}
function stop(){stopped=true;if(timer)clearTimeout(timer)}
function later(ms){if(!stopped)timer=setTimeout(tick,ms)}
function unlock(){
  say("Paid \u2014 unlocking");
  fetch(claim,{method:"POST",headers:{Authorization:"Bearer "+token,"Spikes402-Redeem":"cookie"},credentials:"same-origin",cache:"no-store"}).then(function(r){
    if(r.status!==204)throw new Error("Payment was found, but the page did not accept it ("+r.status+").");
    location.replace(location.href);
  }).catch(function(e){say(e.message||"Could not unlock the page.",true);later(2000)});
}
function tick(){
  if(Date.now()>=exp){say("This invoice has expired. Reload the page for a new one.",true);return}
  fetch(claim,{headers:{Authorization:"Bearer "+token},credentials:"same-origin",cache:"no-store"}).then(function(r){
    if(r.status===204){location.replace(location.href);return}
    if(r.status===202){say("Waiting for payment\u2026");later(1500);return}
    if(r.status===200)return r.json().then(function(j){if(j&&j.status==="paid")unlock();else later(1500)});
    if(r.status===410){say("This invoice has expired. Reload the page for a new one.",true);return}
    if(r.status===409){location.replace(location.href);return}
    if(r.status===402){say("The node reports that less than the price arrived for this invoice. Reload the page for a new one.",true);return}
    later(r.status===429?3000:2000);
  }).catch(function(){later(2000)});
}
say("Waiting for payment\u2026");
tick();
document.addEventListener("visibilitychange",function(){if(document.hidden)stop();else{stopped=false;tick()}});
})();

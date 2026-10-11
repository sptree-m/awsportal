(()=>{"use strict";
// Document listeners survive boosted navigation and resolve the current controls.
function filterRows(){const q=(document.getElementById("instance-filter")?.value||"").toLowerCase(),s=document.getElementById("state-filter")?.value||"";let visible=0;const rows=document.querySelectorAll("tr[data-instance]");rows.forEach(row=>{row.hidden=!!((q&&!row.dataset.search.toLowerCase().includes(q))||(s&&row.dataset.state!==s));if(!row.hidden)visible++});const count=document.getElementById("instance-count");if(count)count.textContent=`${visible} / ${rows.length} instances`;const empty=document.getElementById("instance-empty");if(empty)empty.hidden=visible>0}
function notice(message){let box=document.getElementById("request-notice");if(!box){box=document.createElement("div");box.id="request-notice";box.setAttribute("role","alert");document.body.append(box)}box.textContent=message;box.hidden=false}
document.addEventListener("input",e=>{if(e.target.id==="instance-filter")filterRows()});
document.addEventListener("change",e=>{if(e.target.id==="state-filter")filterRows()});
document.addEventListener("htmx:afterSwap",filterRows);
document.addEventListener("htmx:beforeRequest",e=>{if(document.hidden&&e.detail.elt.matches("[data-transition]")){e.preventDefault();return}const box=document.getElementById("request-notice");if(box)box.hidden=true});
document.addEventListener("visibilitychange",()=>{if(!document.hidden)document.querySelectorAll("[data-transition]").forEach(el=>htmx.trigger(el,"resume"))});
document.addEventListener("htmx:responseError",e=>notice(e.detail.xhr.responseText.trim().slice(0,240)||"処理に失敗しました。再度お試しください。"));
document.addEventListener("htmx:sendError",()=>notice("通信できません。接続を確認して再度お試しください。"));
document.addEventListener("htmx:timeout",()=>notice("応答がありません。更新して状態を確認してください。"));
filterRows();
})();

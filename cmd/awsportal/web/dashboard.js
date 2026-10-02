(()=>{"use strict";
const filter=document.getElementById("instance-filter"),stateFilter=document.getElementById("state-filter");
function filterRows(){const q=(filter?.value||"").toLowerCase(),s=stateFilter?.value||"";document.querySelectorAll("tr[data-instance]").forEach(row=>{row.hidden=!!((q&&!row.dataset.search.toLowerCase().includes(q))||(s&&row.dataset.state!==s))})}
filter?.addEventListener("input",filterRows);stateFilter?.addEventListener("change",filterRows);
document.body.addEventListener("htmx:afterSwap",filterRows);
})();

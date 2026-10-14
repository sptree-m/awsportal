(()=>{'use strict';
const decode=s=>Uint8Array.from(atob(s.replace(/-/g,'+').replace(/_/g,'/')),c=>c.charCodeAt(0));
const encode=b=>b==null?null:btoa(String.fromCharCode(...new Uint8Array(b))).replace(/\+/g,'-').replace(/\//g,'_').replace(/=+$/,'');
async function request(path,data){
 const controller=new AbortController();const timer=setTimeout(()=>controller.abort(),20000);
 try{
  const headers={'Content-Type':'application/json','X-CSRF-Token':document.querySelector('meta[name="csrf-token"]')?.content||''};
  try{const proof=JSON.parse(sessionStorage.getItem('awsportal-stepup')||'null');if(proof?.action===(path.startsWith('/passkeys/register/')?'/passkeys/register':path))headers['X-Step-Up']=proof.token}catch{}
  const response=await fetch(path,{method:'POST',credentials:'same-origin',signal:controller.signal,headers,body:JSON.stringify(data)});
  const reauth=response.headers.get('X-Reauthenticate');if(reauth){location.assign(reauth);throw new Error('操作の本人確認が必要です');}
  if(response.redirected){location.assign(response.url);throw new Error('ログインし直してください');}
  if(!response.ok)throw new Error(await response.text());
  return await response.json();
 }catch(error){if(error.name==='AbortError')throw new Error('通信がタイムアウトしました。接続を確認して再試行してください。');throw error;}
 finally{clearTimeout(timer);}
}
document.addEventListener('submit',async event=>{
 const form=event.target;if(!form.matches('form[data-passkey],form[data-reauth-password]'))return;
 event.preventDefault();const message=form.querySelector('[data-passkey-error]');const button=form.querySelector('button');
 if(button.disabled)return;button.disabled=true;message.textContent='';
 try{
  if(form.matches('[data-reauth-password]')){
   const data=new URLSearchParams(new FormData(form));const response=await fetch('/reauthenticate',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:data});
   if(!response.ok)throw new Error(await response.text());const result=await response.json();sessionStorage.setItem('awsportal-stepup',JSON.stringify({token:result.stepup,action:result.action}));location.assign(result.redirect);return;
  }
  if(!window.isSecureContext||!window.PublicKeyCredential)throw new Error('信頼済みHTTPSとパスキー対応ブラウザーで接続してください');
  const mode=form.dataset.passkey;const data=Object.fromEntries(new FormData(form));
  const options=await request('/passkeys/'+mode+'/begin',data);const key=options.publicKey;
  key.challenge=decode(key.challenge);
  if(mode==='register')key.user.id=decode(key.user.id);
  for(const descriptor of key.allowCredentials||key.excludeCredentials||[])descriptor.id=decode(descriptor.id);
  const credential=mode==='register'?await navigator.credentials.create({publicKey:key}):await navigator.credentials.get({publicKey:key});
  if(!credential)throw new Error('パスキー操作が完了しませんでした');
  const r=credential.response;const response={clientDataJSON:encode(r.clientDataJSON)};
  if(mode==='register'){response.attestationObject=encode(r.attestationObject);response.transports=r.getTransports?r.getTransports():[];}
  else{response.authenticatorData=encode(r.authenticatorData);response.signature=encode(r.signature);response.userHandle=encode(r.userHandle);}
  const result=await request('/passkeys/'+mode+'/finish',{id:credential.id,rawId:encode(credential.rawId),type:credential.type,response,clientExtensionResults:credential.getClientExtensionResults()});
  if(result.stepup)sessionStorage.setItem('awsportal-stepup',JSON.stringify({token:result.stepup,action:result.action}));
  if(result.redirect)location.assign(result.redirect);
 }catch(error){message.textContent=error.name==='NotAllowedError'?'操作がキャンセルされたか、利用できるパスキーがありません。再試行するか管理者へ確認してください。':error.message;}
 finally{button.disabled=false;}
});
})();

(()=>{'use strict';
const decode=s=>Uint8Array.from(atob(s.replace(/-/g,'+').replace(/_/g,'/')),c=>c.charCodeAt(0));
const encode=b=>b==null?null:btoa(String.fromCharCode(...new Uint8Array(b))).replace(/\+/g,'-').replace(/\//g,'_').replace(/=+$/,'');
async function request(path,data){
 const controller=new AbortController();const timer=setTimeout(()=>controller.abort(),20000);
 try{
  const response=await fetch(path,{method:'POST',credentials:'same-origin',signal:controller.signal,headers:{'Content-Type':'application/json','X-CSRF-Token':document.querySelector('meta[name="csrf-token"]')?.content||''},body:JSON.stringify(data)});
  if(response.redirected){location.assign(response.url);throw new Error('ログインし直してください');}
  if(!response.ok)throw new Error(await response.text());
  return await response.json();
 }catch(error){if(error.name==='AbortError')throw new Error('通信がタイムアウトしました。接続を確認して再試行してください。');throw error;}
 finally{clearTimeout(timer);}
}
document.addEventListener('submit',async event=>{
 const form=event.target;if(!form.matches('form[data-passkey]'))return;
 event.preventDefault();const message=form.querySelector('[data-passkey-error]');const button=form.querySelector('button');
 if(button.disabled)return;button.disabled=true;message.textContent='';
 try{
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
  if(result.redirect)location.assign(result.redirect);
 }catch(error){message.textContent=error.name==='NotAllowedError'?'操作がキャンセルされたか、利用できるパスキーがありません。再試行するか管理者へ確認してください。':error.message;}
 finally{button.disabled=false;}
});
})();

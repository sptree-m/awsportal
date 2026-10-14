// UI console tests prove each administrative operation with an enrolled key.
// This helper submits real WebAuthn assertions to the production endpoints.
exports.install=async(page,context)=>{
 const cdp=await context.newCDPSession(page);await cdp.send('WebAuthn.enable');
 const {authenticatorId}=await cdp.send('WebAuthn.addVirtualAuthenticator',{options:{protocol:'ctap2',transport:'internal',hasResidentKey:true,hasUserVerification:true,isUserVerified:true,automaticPresenceSimulation:true}});
 await cdp.send('WebAuthn.addCredential',{authenticatorId,credential:{credentialId:process.env.AWSPORTAL_TEST_KEY_ID,isResidentCredential:true,rpId:'localhost',privateKey:process.env.AWSPORTAL_TEST_PRIVATE_KEY,userHandle:process.env.AWSPORTAL_TEST_USER_HANDLE,signCount:0}});
 await page.addInitScript(()=>{
  const approved=new WeakSet();
  const decode=s=>Uint8Array.from(atob(s.replace(/-/g,'+').replace(/_/g,'/')),c=>c.charCodeAt(0));
  const encode=b=>b==null?null:btoa(String.fromCharCode(...new Uint8Array(b))).replace(/\+/g,'-').replace(/\//g,'_').replace(/=+$/,'');
  document.addEventListener('submit',async event=>{
   const form=event.target;if(form.method.toLowerCase()!=='post')return;
   const action=new URL(form.action,location.href).pathname;
   if(!action.startsWith('/admin/')&&action!=='/mirrors')return;
   if(action==='/mirrors'&&new FormData(form).get('operation')==='sync')return;
   if(approved.has(form)){approved.delete(form);return}
   event.preventDefault();event.stopImmediatePropagation();
   try{
    const request=async(path,body)=>{
     for(let i=0;i<5;i++){
      const r=await fetch(path,{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json','X-CSRF-Token':document.querySelector('meta[name="csrf-token"]').content},body:JSON.stringify(body)});
      if(r.status===429){await new Promise(resolve=>setTimeout(resolve,1000));continue}
      if(!r.ok)throw new Error(await r.text());return r.json();
     }
     throw new Error('reauthentication throttled');
    };
    const options=await request('/passkeys/reauth/begin',{action});const key=options.publicKey;key.challenge=decode(key.challenge);for(const d of key.allowCredentials||[])d.id=decode(d.id);
    const credential=await navigator.credentials.get({publicKey:key});const r=credential.response;
    const result=await request('/passkeys/reauth/finish',{id:credential.id,rawId:encode(credential.rawId),type:credential.type,response:{clientDataJSON:encode(r.clientDataJSON),authenticatorData:encode(r.authenticatorData),signature:encode(r.signature),userHandle:encode(r.userHandle)},clientExtensionResults:credential.getClientExtensionResults()});
    sessionStorage.setItem('awsportal-stepup',JSON.stringify({token:result.stepup,action:result.action}));approved.add(form);form.requestSubmit(event.submitter);
   }catch(error){setTimeout(()=>{throw error},0)}
  },true);
 });
};

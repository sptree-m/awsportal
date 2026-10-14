const assert=require('node:assert/strict');
const crypto=require('node:crypto');
function fixtureTOTP(){let bits=0,value=0,bytes=[];for(const c of 'JBSWY3DPEHPK3PXP'){value=(value<<5)|'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'.indexOf(c);bits+=5;if(bits>=8){bits-=8;bytes.push((value>>>bits)&255);value&=(1<<bits)-1}}const counter=Buffer.alloc(8);counter.writeBigUInt64BE(BigInt(Math.floor(Date.now()/30000)));const mac=crypto.createHmac('sha1',Buffer.from(bytes)).update(counter).digest();const offset=mac[mac.length-1]&15;return String((mac.readUInt32BE(offset)&0x7fffffff)%1000000).padStart(6,'0')}
const {chromium}=require('playwright');
(async()=>{
 const browser=await chromium.launch({headless:true});
 try{
  const context=await browser.newContext({viewport:{width:1366,height:768}});
  const page=await context.newPage();const errors=[];const external=[];
  page.on('response',async r=>{if(r.status()>=400){let text='';try{text=await r.text()}catch{}console.error('HTTP '+r.status()+' '+new URL(r.url()).pathname+' '+text.slice(0,160))}});
  page.on('pageerror',e=>errors.push(e.message));page.on('request',r=>{if(!r.url().startsWith(process.env.AWSPORTAL_TEST_URL))external.push(r.url())});
  const cdp=await context.newCDPSession(page);await cdp.send('WebAuthn.enable');
  let authenticator=await cdp.send('WebAuthn.addVirtualAuthenticator',{options:{protocol:'ctap2',transport:'internal',hasResidentKey:true,hasUserVerification:true,isUserVerified:true,automaticPresenceSimulation:true}});
  const base=process.env.AWSPORTAL_TEST_URL;
  for(const role of ['user','portal_admin']){
   await page.waitForTimeout(5000);
   await page.goto(base+'/passkeys/enroll');
   await page.locator('[name=username]').fill(role);await page.locator('[name=code]').fill(process.env[role==='user'?'AWSPORTAL_TEST_USER_CODE':'AWSPORTAL_TEST_ADMIN_CODE']);await page.locator('[name=name]').fill('Company PC');
   await page.locator('form[data-passkey] button').click();await page.waitForURL(base+'/login');
   assert.equal((await context.cookies()).filter(c=>c.name==='awsportal_session').length,0,'enrollment must not create a session');
   await page.locator('form[data-passkey] [name=username]').fill(role);await page.locator('form[data-passkey] button').click();await page.waitForURL(base+'/passkeys');
   assert.equal(await page.locator('tbody tr').count(),1);assert.equal(await page.locator('nav a[href="/mfa"]').count(),role==='portal_admin'?1:0);
   await page.locator('form[data-passkey] [name=name]').fill('Second device credential');await page.locator('form[data-passkey] button').click();await page.waitForURL(/\/reauthenticate\?/);
   await page.waitForTimeout(3000);await page.locator('form[data-passkey="reauth"] button').click();await page.waitForURL(base+'/passkeys');
   await cdp.send('WebAuthn.removeVirtualAuthenticator',{authenticatorId:authenticator.authenticatorId});authenticator=await cdp.send('WebAuthn.addVirtualAuthenticator',{options:{protocol:'ctap2',transport:'internal',hasResidentKey:true,hasUserVerification:true,isUserVerified:true,automaticPresenceSimulation:true}});
   await page.locator('form[data-passkey] [name=name]').fill('Second device credential');await page.locator('form[data-passkey] button').click();await page.waitForURL(base+'/login');
   await page.locator('form[data-passkey] [name=username]').fill(role);await page.locator('form[data-passkey] button').click();await page.waitForURL(base+'/passkeys');
   assert.equal(await page.locator('tbody tr').count(),2,'multiple device credentials');
   for(const [width,height] of [[1366,768],[1920,1080],[2560,1440],[3840,2160],[390,844]]){
    await page.setViewportSize({width,height});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true,`passkey overflow ${width}`);
    if(process.env.AWSPORTAL_SCREENSHOT_DIR)await page.screenshot({path:`${process.env.AWSPORTAL_SCREENSHOT_DIR}/passkeys-${role}-${width}x${height}.png`,fullPage:true});
   }
   await page.locator('tbody tr').first().locator('button').click();await page.waitForURL(/\/reauthenticate\?/);
   await page.waitForTimeout(3000);await page.locator('form[data-passkey="reauth"] button').click();await page.waitForURL(base+'/passkeys');
   await page.locator('tbody tr').first().locator('button').click();await page.waitForURL(base+'/login');
   await context.clearCookies();
  }
  await page.goto(base+'/login');await page.locator('details summary').first().click();await page.locator('form[action="/login"] [name=username]').fill('portal_admin');await page.locator('form[action="/login"] [name=password]').fill('Correct-Password-42');await page.locator('form[action="/login"] [name=totp]').fill(fixtureTOTP());await page.locator('form[action="/login"] button').click();await page.waitForURL(base+'/passkeys');
  await context.clearCookies();await page.goto(base+'/login');
  await page.route('**/passkeys/login/begin',()=>{});
  await page.locator('form[data-passkey] [name=username]').fill('user');await page.locator('form[data-passkey] button').click();
  await page.locator('[data-passkey-error]').filter({hasText:'通信がタイムアウトしました'}).waitFor({state:'visible',timeout:25000});
  assert.equal(await page.locator('form[data-passkey] button').isEnabled(),true,'timeout must allow retry');await page.unroute('**/passkeys/login/begin');
  assert.deepEqual(errors,[]);assert.deepEqual(external,[]);
  await cdp.send('WebAuthn.removeVirtualAuthenticator',{authenticatorId:authenticator.authenticatorId});
  console.log('PASS: enrollment, no enrollment session, user/admin passkey login, admin TOTP, multiple keys, deletion, timeout/retry, five widths, CSP and local resources');
 }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exit(1)});

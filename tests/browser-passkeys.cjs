const assert=require('node:assert/strict');
const {chromium}=require('playwright');
(async()=>{
 const browser=await chromium.launch({headless:true});
 try{
  const context=await browser.newContext({viewport:{width:1366,height:768}});
  const page=await context.newPage();const errors=[];const external=[];
  page.on('response',async r=>{if(r.status()>=400)console.error('HTTP '+r.status()+' '+new URL(r.url()).pathname+' '+(await r.text()).slice(0,160))});
  page.on('pageerror',e=>errors.push(e.message));page.on('request',r=>{if(!r.url().startsWith(process.env.AWSPORTAL_TEST_URL))external.push(r.url())});
  const cdp=await context.newCDPSession(page);await cdp.send('WebAuthn.enable');
  let authenticator=await cdp.send('WebAuthn.addVirtualAuthenticator',{options:{protocol:'ctap2',transport:'internal',hasResidentKey:true,hasUserVerification:true,isUserVerified:true,automaticPresenceSimulation:true}});
  const base=process.env.AWSPORTAL_TEST_URL;
  for(const role of ['user','portal_admin']){
   await page.goto(base+'/passkeys/enroll');
   await page.locator('[name=username]').fill(role);await page.locator('[name=code]').fill(process.env[role==='user'?'AWSPORTAL_TEST_USER_CODE':'AWSPORTAL_TEST_ADMIN_CODE']);await page.locator('[name=name]').fill('Company PC');
   await page.locator('form[data-passkey] button').click();await page.waitForURL(base+'/login');
   assert.equal((await context.cookies()).filter(c=>c.name==='awsportal_session').length,0,'enrollment must not create a session');
   await page.locator('form[data-passkey] [name=username]').fill(role);await page.locator('form[data-passkey] button').click();await page.waitForURL(base+'/passkeys');
   assert.equal(await page.locator('tbody tr').count(),1);assert.equal(await page.locator('nav a[href="/mfa"]').count(),role==='portal_admin'?1:0);
   await cdp.send('WebAuthn.removeVirtualAuthenticator',{authenticatorId:authenticator.authenticatorId});authenticator=await cdp.send('WebAuthn.addVirtualAuthenticator',{options:{protocol:'ctap2',transport:'internal',hasResidentKey:true,hasUserVerification:true,isUserVerified:true,automaticPresenceSimulation:true}});
   await page.locator('form[data-passkey] [name=name]').fill('Second device credential');await page.locator('form[data-passkey] button').click();await page.waitForURL(base+'/login');
   await page.locator('form[data-passkey] [name=username]').fill(role);await page.locator('form[data-passkey] button').click();await page.waitForURL(base+'/passkeys');
   assert.equal(await page.locator('tbody tr').count(),2,'multiple device credentials');
   for(const [width,height] of [[1366,768],[1920,1080],[2560,1440],[3840,2160],[390,844]]){
    await page.setViewportSize({width,height});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true,`passkey overflow ${width}`);
    if(process.env.AWSPORTAL_SCREENSHOT_DIR)await page.screenshot({path:`${process.env.AWSPORTAL_SCREENSHOT_DIR}/passkeys-${role}-${width}x${height}.png`,fullPage:true});
   }
   await page.locator('tbody tr').first().locator('button').click();await page.waitForURL(base+'/login');
   await context.clearCookies();
  }
  await page.goto(base+'/login');await page.locator('details summary').first().click();await page.locator('form[action="/login"] [name=username]').fill('portal_admin');await page.locator('form[action="/login"] [name=password]').fill('Correct-Password-42');await page.locator('form[action="/login"] [name=totp]').fill(process.env.AWSPORTAL_TEST_TOTP);await page.locator('form[action="/login"] button').click();await page.waitForURL(base+'/passkeys');
  assert.deepEqual(errors,[]);assert.deepEqual(external,[]);
  await cdp.send('WebAuthn.removeVirtualAuthenticator',{authenticatorId:authenticator.authenticatorId});
  console.log('PASS: enrollment, no enrollment session, user/admin passkey login, admin TOTP, multiple keys, deletion, five widths, CSP and local resources');
 }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exit(1)});

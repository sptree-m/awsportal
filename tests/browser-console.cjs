const {chromium}=require('playwright');
const assert=require('node:assert/strict');
(async()=>{
 const browser=await chromium.launch({headless:true,args:['--no-sandbox']});
 try{
 const context=await browser.newContext();const base=process.argv[2];
 await context.addCookies([{name:'awsportal_session',value:'browser-session',url:base}]);
 const page=await context.newPage();const errors=[],external=[];let documents=0,requests=0;
 page.on('pageerror',e=>errors.push(e.message));page.on('request',r=>{requests++;if(r.isNavigationRequest())documents++;if(!r.url().startsWith(base))external.push(r.url())});
 await page.goto(base);await page.locator('nav a[href="/instances"]').click();await page.waitForURL(base+'/instances');
 await page.locator('#instance-filter').fill('ADAS');assert.equal(await page.locator('tr[data-instance]:visible').count(),1);
 await Promise.all([page.waitForResponse(r=>r.url()===base+'/instances'&&r.request().headers()['hx-target']==='instances-live'),page.getByRole('button',{name:'更新'}).click()]);
 await page.waitForTimeout(100);assert.equal(await page.locator('#instances-live').count(),1);assert.equal(await page.locator('#instance-filter').inputValue(),'ADAS');assert.equal(await page.locator('tr[data-instance]:visible').count(),1);
 await page.locator('#instance-filter').fill('no-match');assert.equal(await page.locator('#instance-empty:visible').count(),1);
 await page.locator('#instance-filter').fill('');
 await page.locator('tr[data-instance="i-dev"] a', {hasText:'詳細'}).click();await page.waitForURL(base+'/instances/i-dev');
 await page.getByRole('button',{name:'起動',exact:true}).click();await page.waitForSelector('#instance-live [action$="/stop"]');
 assert.equal(await page.locator('#instance-live [data-transition]').count(),0);
 await page.waitForTimeout(100);const idle=requests;await page.waitForTimeout(1800);assert.equal(requests,idle,'stable state must not poll');
 await page.locator('nav a[href="/instances"]').click();await page.waitForURL(base+'/instances');
 await page.locator('#state-filter').selectOption('running');assert.equal(await page.locator('tr[data-instance]:visible').count(),2);
 await page.locator('nav a[href="/"]').click();await page.waitForURL(base+'/');await page.locator('nav a[href="/instances"]').click();await page.waitForURL(base+'/instances');
 await page.locator('#instance-filter').fill('CV');assert.equal(await page.locator('tr[data-instance]:visible').count(),1);
 // Server failures stay visible without replacing the table or executing response markup.
 await page.route('**/instances',route=>route.request().headers()['hx-target']==='instances-live'?route.fulfill({status:502,contentType:'text/plain',body:'AWS状態を取得できません'}):route.continue());
 await page.getByRole('button',{name:'更新'}).click();await page.waitForSelector('#request-notice');assert.match(await page.locator('#request-notice').textContent(),/AWS/);assert.equal(await page.locator('tr[data-instance]').count(),2);await page.unroute('**/instances');
 await page.locator('nav a[href="/costs"]').click();await page.waitForURL(base+'/costs');
 await page.locator('input[name="month"]').fill('2026-09');await page.getByRole('button',{name:'表示',exact:true}).click();await page.waitForURL(base+'/costs?month=2026-09');assert.equal(await page.locator('input[name="month"]').inputValue(),'2026-09');
 await page.locator('nav a[href="/instances"]').click();await page.waitForURL(base+'/instances');await page.evaluate(()=>document.fonts.ready);
 for(const [width,height] of [[1366,768],[1920,1080],[2560,1440],[3840,2160]]){
  await page.setViewportSize({width,height});await page.locator('#instance-filter').fill('');
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true,`page overflow at ${width}`);
  assert.equal(await page.locator('aside').count(),1);
  if(process.env.AWSPORTAL_SCREENSHOT_DIR)await page.screenshot({path:`${process.env.AWSPORTAL_SCREENSHOT_DIR}/instances-${width}x${height}.png`});
 }
 assert.equal(documents,1,'boosted navigation must not reload document');assert.deepEqual(errors,[]);assert.deepEqual(external,[]);
 console.log('PASS: boosted navigation, repeated search, partial refresh, detail action/poll termination, idle traffic, errors, cost month/history URL, four resolutions, no external requests');
 }finally{await browser.close()}
})().catch(e=>{console.error(e);process.exit(1)});

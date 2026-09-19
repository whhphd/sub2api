import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import ModelHoldBadge from '../ModelHoldBadge.vue'
vi.mock('vue-i18n',()=>({useI18n:()=>({t:(key:string)=>key})}))
let wrapper: VueWrapper | undefined
afterEach(()=>{wrapper?.unmount();wrapper=undefined;document.body.innerHTML=''})
describe('compact model hold badge',()=>{
 it('keeps full model names out of the row and opens details on click',async()=>{
  wrapper=mount(ModelHoldBadge,{attachTo:document.body,props:{accountId:1,models:['gpt-a','gpt-b','gpt-c']}})
  const button=wrapper.get('[data-testid="model-hold-badge"]')
  expect(button.text()).toContain('3');expect(button.text()).not.toContain('gpt-')
  await button.trigger('click');expect(document.querySelector('[role="dialog"]')?.textContent).toContain('gpt-a')
  await button.trigger('mouseleave');await new Promise(r=>setTimeout(r,170));expect(document.querySelector('[role="dialog"]')).not.toBeNull()
  document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape'}));await flushPromises();expect(document.querySelector('[role="dialog"]')).toBeNull()
 })
 it('opens on focus and closes on an outside click or account change',async()=>{
  wrapper=mount(ModelHoldBadge,{attachTo:document.body,props:{accountId:1,models:['model']}})
  await wrapper.get('button').trigger('focus');expect(document.querySelector('[role="dialog"]')).not.toBeNull()
  document.body.click();await flushPromises();expect(document.querySelector('[role="dialog"]')).toBeNull()
  await wrapper.get('button').trigger('click');await wrapper.setProps({accountId:2});expect(document.querySelector('[role="dialog"]')).toBeNull()
 })
 it('hides when no models are held',()=>{
  wrapper=mount(ModelHoldBadge,{props:{accountId:1,models:[]}});expect(wrapper.find('button').exists()).toBe(false)
 })
})

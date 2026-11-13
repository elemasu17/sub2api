import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const settings = vi.hoisted(() => ({ contact_info: '', redeem_purchase_url: '' }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({
  user: { id: 42, balance: 1, concurrency: 1 }, token: 'must-not-leak',
}) }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }) }))
vi.mock('@/stores/subscriptions', () => ({ useSubscriptionStore: () => ({}) }))
vi.mock('@/api', () => ({
  authAPI: { getPublicSettings: async () => settings },
  redeemAPI: { getHistory: async () => ({ items: [], total: 0 }) },
}))

import RedeemView from '../RedeemView.vue'

const render = () => mount(RedeemView, { global: { stubs: {
  AppLayout: { template: '<main><slot /></main>' }, Icon: true,
} } })

describe('redeem purchase link', () => {
  beforeEach(() => { settings.redeem_purchase_url = '' })

  it('hides the link when no destination is configured', async () => {
    const wrapper = render()
    await flushPromises()
    expect(wrapper.find('a[target="_blank"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('opens the exact configured URL without adding user credentials', async () => {
    settings.redeem_purchase_url = 'https://shop.example.com/buy?channel=sub2api'
    const wrapper = render()
    await flushPromises()
    const link = wrapper.get('a[target="_blank"]')
    expect(link.attributes('href')).toBe(settings.redeem_purchase_url)
    expect(link.attributes('rel')).toBe('noopener noreferrer')
    expect(link.text()).toBe('redeem.purchaseCode')
    expect(link.attributes('href')).not.toContain('must-not-leak')
    expect(wrapper.find('form').exists()).toBe(true)
    wrapper.unmount()
  })
})

import { emptyApi as api } from "./empty-api";
const injectedRtkApi = api.injectEndpoints({
  endpoints: (build) => ({
    register: build.mutation<RegisterApiResponse, RegisterApiArg>({
      query: (queryArg) => ({
        url: `/workspaces/register`,
        method: "POST",
        body: queryArg.registerRequest,
      }),
    }),
    login: build.mutation<LoginApiResponse, LoginApiArg>({
      query: (queryArg) => ({
        url: `/workspaces/login`,
        method: "POST",
        body: queryArg.loginRequest,
      }),
    }),
  }),
  overrideExisting: false,
});
export { injectedRtkApi as api };
export type RegisterApiResponse = /** status 200 Session token */ TokenResponse;
export type RegisterApiArg = {
  registerRequest: RegisterRequest;
};
export type LoginApiResponse = /** status 200 Session token */ TokenResponse;
export type LoginApiArg = {
  loginRequest: LoginRequest;
};
export type TokenResponse = {
  token: string;
  workspace_id: string;
  user_id: string;
};
export type RegisterRequest = {
  workspace_name: string;
  email: string;
  password: string;
};
export type LoginRequest = {
  email: string;
  password: string;
};
export const { useRegisterMutation, useLoginMutation } = injectedRtkApi;

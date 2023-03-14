const apiServer = process.env.REACT_APP_API_SERVER || "";
const apiVersion = "v1";

const config = {
    apiServer: apiServer,
    apiVersion: apiVersion,
    apiUri: `${apiServer}/api/${apiVersion}`,
};

export default config;
